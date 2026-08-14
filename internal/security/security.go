// Package security provides password hashing, signed sessions,
// IP whitelist matching, and sliding-window rate limiting.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ── Password hashing (PBKDF2-SHA256) ────────────────────────

const (
	pbkdf2Iter = 200_000
	pbkdf2Key  = 32
	pbkdf2Salt = 16
)

// pbkdf2KeyDeriver derives a key using PBKDF2-HMAC-SHA256 (RFC 2898).
// Standard library crypto/pbkdf2 is not exposed prior to Go 1.25, so we
// implement it directly using HMAC-SHA256.
func pbkdf2KeyDeriver(password, salt []byte, iter, keyLen int, h func() hash.Hash) []byte {
	prf := hmac.New(h, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	U := make([]byte, hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf[:4])
		dk = prf.Sum(dk)
		T := dk[len(dk)-hashLen:]
		copy(U, T)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(U)
			U = U[:0]
			U = prf.Sum(U)
			for x := range U {
				T[x] ^= U[x]
			}
		}
	}
	return dk[:keyLen]
}

// HashPassword returns "pbkdf2_sha256$iter$salt$hash".
func HashPassword(password string) string {
	salt := make([]byte, pbkdf2Salt)
	_, _ = rand.Read(salt)
	dk := pbkdf2KeyDeriver([]byte(password), salt, pbkdf2Iter, pbkdf2Key, sha256.New)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", pbkdf2Iter, hex.EncodeToString(salt), hex.EncodeToString(dk))
}

func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	var iter int
	fmt.Sscanf(parts[1], "%d", &iter)
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(parts[3])
	if err != nil {
		return false
	}
	dk := pbkdf2KeyDeriver([]byte(password), salt, iter, len(expected), sha256.New)
	return subtle.ConstantTimeCompare(dk, expected) == 1
}

// GenerateSecret returns a URL-safe random secret (48 bytes → 64 chars).
func GenerateSecret() string {
	b := make([]byte, 48)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ── Session tokens (HMAC-signed, stateless) ─────────────────

type Session struct {
	secret []byte
}

type SessionClaims struct {
	User string `json:"u"`
	IAT  int64  `json:"iat"`
	EXP  int64  `json:"exp"`
}

func NewSession(secret string) *Session {
	return &Session{secret: []byte(secret)}
}

func (s *Session) Create(username string, ttlMinutes int) string {
	claims := SessionClaims{
		User: username,
		IAT:  time.Now().Unix(),
		EXP:  time.Now().Add(time.Duration(ttlMinutes) * time.Minute).Unix(),
	}
	raw, _ := json.Marshal(claims)
	sig := s.sign(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + sig
}

func (s *Session) Verify(token string) (*SessionClaims, error) {
	idx := strings.LastIndex(token, ".")
	if idx < 0 {
		return nil, errors.New("malformed token")
	}
	rawB64, sig := token[:idx], token[idx+1:]
	raw, err := base64.RawURLEncoding.DecodeString(rawB64)
	if err != nil {
		return nil, err
	}
	expected := s.sign(raw)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return nil, errors.New("bad signature")
	}
	var claims SessionClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	if time.Now().Unix() > claims.EXP {
		return nil, errors.New("session expired")
	}
	return &claims, nil
}

func (s *Session) sign(raw []byte) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write(raw)
	return hex.EncodeToString(m.Sum(nil))
}

// ── IP whitelist ────────────────────────────────────────────

// IPInList reports whether ip matches any CIDR or exact address in list.
func IPInList(ip string, list []string) bool {
	if len(list) == 0 {
		return false
	}
	addr, err := netip.ParseAddr(strings.Split(ip, ":")[0])
	if err != nil {
		return false
	}
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				continue
			}
			if prefix.Contains(addr) {
				return true
			}
		} else {
			a, err := netip.ParseAddr(entry)
			if err == nil && a == addr {
				return true
			}
		}
	}
	return false
}

// ClientIP extracts the real client IP, honoring X-Forwarded-For when behind proxy.
func ClientIP(remoteAddr, xff string) string {
	if xff != "" {
		// First IP in chain is the original client
		if idx := strings.Index(xff, ","); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// ── Sliding-window rate limiter ─────────────────────────────

type RateLimiter struct {
	mu       sync.Mutex
	hits     map[string][]time.Time
	max      int
	window   time.Duration
}

func NewRateLimiter(maxPerMinute int) *RateLimiter {
	if maxPerMinute <= 0 {
		maxPerMinute = 30
	}
	rl := &RateLimiter{
		hits:   make(map[string][]time.Time),
		max:    maxPerMinute,
		window: time.Minute,
	}
	go rl.gc()
	return rl
}

// Check returns (allowed, retryAfterSeconds).
func (rl *RateLimiter) Check(key string) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	hits := rl.hits[key]
	out := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	if len(out) >= rl.max {
		retry := int(rl.window.Seconds() - now.Sub(out[0]).Seconds()) + 1
		rl.hits[key] = out
		return false, retry
	}
	out = append(out, now)
	rl.hits[key] = out
	return true, 0
}

func (rl *RateLimiter) Reset(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.hits, key)
}

func (rl *RateLimiter) UpdateLimit(max int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.max = max
}

func (rl *RateLimiter) gc() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-rl.window)
		for k, hits := range rl.hits {
			out := hits[:0]
			for _, t := range hits {
				if t.After(cutoff) {
					out = append(out, t)
				}
			}
			if len(out) == 0 {
				delete(rl.hits, k)
			} else {
				rl.hits[k] = out
			}
		}
		rl.mu.Unlock()
	}
}
