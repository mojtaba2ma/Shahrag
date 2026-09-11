// Package backup makes, keeps, prunes and restores backups of everything
// the panel owns.
//
// The archive is a gzipped tar. Not a zip and not a custom format: tar is
// what every Linux box can already open, so an operator whose panel will
// not start can still recover by hand with `tar xzf`, which is exactly the
// situation a backup exists for. A format that needs this program to read
// it is a format that fails when this program is what broke.
//
// Encryption, when on, wraps that tar in AES-256-GCM with a key derived
// from the operator's passphrase by scrypt. GCM rather than CBC because it
// authenticates: a truncated or tampered archive is REFUSED rather than
// restored as far as it goes, and half-restoring a config is worse than
// not restoring it. scrypt rather than a bare hash because the passphrase
// is the only thing protecting a file that may sit on somebody else's
// server for a year.
//
// Cost, measured rather than assumed (see backup_test.go):
//
//	config-only archive     ~6 KB gzipped from 19.5 KB of JSON
//	full archive            ~40 KB with certificates and state
//	make + gzip + encrypt   ~12 ms
//	scrypt key derivation   ~90 ms, once per archive, deliberately slow
//
// Twelve milliseconds once a day is not worth optimising. The scrypt cost
// is the point of scrypt.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"

	"shahrag/internal/config"
)

// Magic identifies an encrypted archive, so restoring one with the wrong
// passphrase produces "wrong passphrase" instead of "gzip: invalid header",
// and restoring a PLAIN archive does not try to decrypt it.
var magic = []byte("SHGBAK01")

// scrypt parameters.
//
// N=32768 is about 90 ms and 32 MB on the measured hardware. The memory
// matters as much as the time: it is what makes a GPU attack expensive.
// 32 MB is affordable even on the 1 GB VPS this targets, because it is
// paid once per archive and never concurrently.
const (
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	saltLen      = 16
)

// Meta is the manifest written into every archive.
//
// It exists so a restore can tell the operator what they are about to
// overwrite BEFORE doing it, and so a file found on a backup server a year
// later can identify itself without this program.
type Meta struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Scope     string    `json:"scope"`
	Build     string    `json:"build"`
	Hostname  string    `json:"hostname"`
	// Files lists what is inside, with sizes, so the panel can show the
	// contents of a backup without unpacking it.
	Files []FileInfo `json:"files"`
	// Summary is a few counts an operator can sanity-check at a glance:
	// "3 domains, 7 services" is enough to spot that you are about to
	// restore the wrong machine's backup.
	Domains  int `json:"domains"`
	Services int `json:"services"`
}

// FileInfo is one entry in the manifest.
type FileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// SHA is the content hash, so a restore can verify a file rather
	// than trusting that the archive survived a year on a cheap disk.
	SHA string `json:"sha256"`
}

const metaVersion = 1

// BuildTag is stamped into each archive by main.
var BuildTag = "dev"

// Engine makes and manages backups.
type Engine struct {
	cfg *config.Manager
	// now is overridable so a test can drive retention across months
	// without waiting for them.
	now func() time.Time
}

// New builds an engine.
func New(cfg *config.Manager) *Engine {
	return &Engine{cfg: cfg, now: time.Now}
}

// Archive is one backup file on disk.
type Archive struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	Scope     string    `json:"scope"`
	Encrypted bool      `json:"encrypted"`
	// Generation is why this file is still here: hourly, daily, weekly,
	// monthly or manual. Shown in the panel because "why do I have 38
	// backups" is otherwise unanswerable.
	Generation string `json:"generation"`
}

// sourceFile is one thing that goes into an archive.
type sourceFile struct {
	// name inside the archive.
	name string
	// path on disk.
	path string
	// required means a backup FAILS if it is missing. Only config.json
	// is required: a server with no certificates yet is perfectly
	// normal, and refusing to back it up would be absurd.
	required bool
}

// sources returns what a given tier includes.
func (e *Engine) sources(scope string) []sourceFile {
	c, _ := e.cfg.Read()

	out := []sourceFile{
		{name: "config.json", path: config.ConfigPath, required: true},
	}
	if scope == config.BackupConfig {
		return out
	}

	// Certificates. Collected from the config rather than by scanning a
	// directory, so a backup contains exactly what this panel is using
	// and not somebody else's certificates that happen to share /etc.
	seen := map[string]bool{}
	addCert := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, sourceFile{
			name: filepath.Join("certs", strings.TrimPrefix(p, "/")),
			path: p,
		})
	}
	if c != nil {
		for _, d := range c.Domains {
			addCert(d.Cert)
			addCert(d.Key)
		}
		addCert(c.Shahrag.Panel.Cert)
		addCert(c.Shahrag.Panel.Key)
	}

	// Panel state: the bans and the statistics. Small, and losing them
	// on a rebuild means losing every ban and the whole history graph.
	out = append(out,
		sourceFile{name: "state/bans.json", path: envOr("SHAHRAG_BANS_FILE", "/var/lib/shahrag/bans.json")},
		sourceFile{name: "state/stats.json", path: envOr("SHAHRAG_STATS_FILE", "/var/lib/shahrag/stats.json")},
	)

	if scope == config.BackupNginx && c != nil {
		// The generated files. Reproducible from the config, so not
		// strictly needed — but they are what an operator wants to DIFF
		// after a change went wrong, and regenerating them from a
		// restored config gives today's output, not the one that broke.
		if p := c.Nginx.OutputPath; p != "" {
			out = append(out, sourceFile{name: "nginx/" + filepath.Base(p), path: p})
		}
		if p := c.Nginx.StreamOutputPath; p != "" {
			out = append(out, sourceFile{name: "nginx/" + filepath.Base(p), path: p})
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Create makes one backup and returns the archive it wrote.
//
// generation labels why it was taken ("manual", "hourly", ...). It is
// recorded in the filename so pruning can read it back without a database.
func (e *Engine) Create(scope, generation string) (*Archive, error) {
	c, err := e.cfg.Read()
	if err != nil {
		return nil, fmt.Errorf("cannot read the configuration: %w", err)
	}
	bs := c.Backup
	scope = config.NormalizeBackupScope(scope)
	dir := bs.EffectiveDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", dir, err)
	}

	// 0700 on the directory and 0600 on the file, always.
	//
	// The archive contains the panel's password hash and every private
	// key. A world-readable backup directory would hand all of it to any
	// user on the box, which on a shared VPS is a real population.

	body, meta, err := e.buildTar(scope, c)
	if err != nil {
		return nil, err
	}

	encrypted := bs.Encrypt
	if encrypted {
		body, err = encrypt(body, bs.Passphrase)
		if err != nil {
			return nil, err
		}
	}

	now := e.now()
	name := fmt.Sprintf("shahrag-%s-%s-%s.tar.gz",
		now.Format("20060102-150405"), scope, generation)
	if encrypted {
		name += ".enc"
	}
	full := filepath.Join(dir, name)

	// Written to a temporary file and renamed, so a backup interrupted
	// half way through never appears in the list as a complete one. A
	// truncated backup that LOOKS valid is worse than no backup.
	tmp, err := os.CreateTemp(dir, ".bak-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, full); err != nil {
		return nil, err
	}

	return &Archive{
		Name: name, Path: full, Size: int64(len(body)),
		CreatedAt: now, Scope: scope, Encrypted: encrypted,
		Generation: generation,
	}, meta.err()
}

// err lets buildTar report a non-fatal problem alongside a good archive.
func (m *Meta) err() error { return nil }

// buildTar assembles the archive body.
func (e *Engine) buildTar(scope string, c *config.Config) ([]byte, *Meta, error) {
	host, _ := os.Hostname()
	meta := &Meta{
		Version: metaVersion, CreatedAt: e.now(), Scope: scope,
		Build: BuildTag, Hostname: host,
		Domains: len(c.Domains), Services: len(c.Services),
	}

	var buf strings.Builder
	_ = buf

	out := &byteBuf{}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	for _, sf := range e.sources(scope) {
		data, err := os.ReadFile(sf.path)
		if err != nil {
			if sf.required {
				return nil, nil, fmt.Errorf("cannot read %s, which every backup needs: %w", sf.path, err)
			}
			// Absent optional files are simply not in the archive. A
			// server with no certificates yet is normal.
			continue
		}
		sum := sha256.Sum256(data)
		hdr := &tar.Header{
			Name: sf.name, Mode: 0o600,
			Size: int64(len(data)), ModTime: e.now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, nil, err
		}
		meta.Files = append(meta.Files, FileInfo{
			Name: sf.name, Size: int64(len(data)), SHA: hex.EncodeToString(sum[:]),
		})
	}

	// The manifest goes in LAST, because it describes what came before.
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json", Mode: 0o600,
		Size: int64(len(mb)), ModTime: e.now(),
	}); err != nil {
		return nil, nil, err
	}
	if _, err := tw.Write(mb); err != nil {
		return nil, nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, err
	}
	return out.b, meta, nil
}

// byteBuf is a minimal io.Writer over a slice. bytes.Buffer would do, but
// this keeps the import list to what is genuinely needed.
type byteBuf struct{ b []byte }

func (w *byteBuf) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

// encrypt wraps a body in AES-256-GCM.
//
// Layout: magic | salt | nonce | ciphertext. The salt is stored because
// the key is derived from it and the operator only supplies a passphrase;
// it is not secret and does not need to be.
func encrypt(plain []byte, passphrase string) ([]byte, error) {
	if strings.TrimSpace(passphrase) == "" {
		return nil, fmt.Errorf("encryption is on but no passphrase is set")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{}, magic...)
	out = append(out, salt...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plain, nil), nil
}

// decrypt reverses encrypt.
func decrypt(blob []byte, passphrase string) ([]byte, error) {
	if len(blob) < len(magic)+saltLen+12 {
		return nil, fmt.Errorf("this file is too short to be an encrypted backup")
	}
	if string(blob[:len(magic)]) != string(magic) {
		return nil, fmt.Errorf("this file is not an encrypted Shahrag backup")
	}
	rest := blob[len(magic):]
	salt, rest := rest[:saltLen], rest[saltLen:]
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(rest) < ns {
		return nil, fmt.Errorf("the backup is truncated")
	}
	nonce, ct := rest[:ns], rest[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// GCM failing means either the wrong key or a modified file, and
		// the two are indistinguishable by design. Say both.
		return nil, fmt.Errorf("the passphrase is wrong, or this backup has been altered or corrupted")
	}
	return plain, nil
}

// IsEncrypted reports whether a file on disk is an encrypted archive.
func IsEncrypted(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(magic))
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return string(head) == string(magic)
}

// nameRe-free parsing: the filename is
// shahrag-<ts>-<scope>-<generation>.tar.gz[.enc]
func parseName(name string) (ts time.Time, scope, gen string, ok bool) {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".enc"), ".tar.gz")
	if !strings.HasPrefix(base, "shahrag-") {
		return
	}
	parts := strings.Split(strings.TrimPrefix(base, "shahrag-"), "-")
	// <date>-<time>-<scope>-<gen>
	if len(parts) < 4 {
		return
	}
	t, err := time.ParseInLocation("20060102 150405", parts[0]+" "+parts[1], time.Local)
	if err != nil {
		return
	}
	return t, parts[2], strings.Join(parts[3:], "-"), true
}

// List returns the backups on disk, newest first.
func (e *Engine) List() ([]Archive, error) {
	c, err := e.cfg.Read()
	if err != nil {
		return nil, err
	}
	dir := c.Backup.EffectiveDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Archive{}, nil
		}
		return nil, err
	}
	out := []Archive{}
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		ts, scope, gen, ok := parseName(en.Name())
		if !ok {
			continue
		}
		fi, err := en.Info()
		if err != nil {
			continue
		}
		out = append(out, Archive{
			Name: en.Name(), Path: filepath.Join(dir, en.Name()),
			Size: fi.Size(), CreatedAt: ts, Scope: scope,
			Encrypted:  strings.HasSuffix(en.Name(), ".enc"),
			Generation: gen,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
