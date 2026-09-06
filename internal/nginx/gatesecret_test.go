package nginx

// The generated access key.
//
// This key is compared by nginx as a plain string against a cookie. There
// is no hashing and no work factor to slow an attacker down — the length
// and the randomness ARE the security, so both are asserted here.

import (
	"math"
	"strings"
	"testing"
)

func TestGeneratedKeyPassesItsOwnValidator(t *testing.T) {
	// The generator and the validator must agree, or the panel would hand
	// out keys it then refuses to save.
	for i := 0; i < 200; i++ {
		k, err := NewGateSecret()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidGateSecret(k) {
			t.Fatalf("generated key %q is rejected by ValidGateSecret", k)
		}
		if len(k) != GateSecretLength {
			t.Fatalf("key length %d, want %d", len(k), GateSecretLength)
		}
	}
}

// The key ends up inside a generated nginx string and inside a Set-Cookie
// header. A quote, a dollar or a semicolon in it would break the config.
func TestGeneratedKeyIsSafeInsideTheGeneratedConfig(t *testing.T) {
	for i := 0; i < 200; i++ {
		k, _ := NewGateSecret()
		if strings.ContainsAny(k, "$'\";{} \t\r\n\\") {
			t.Fatalf("key %q contains a character that breaks the nginx config or the cookie header", k)
		}
		if !gateSafeHTML(k) {
			t.Fatalf("key %q would be rejected by the challenge-page guard", k)
		}
	}
}

// Keys must not repeat. A generator seeded from the clock would produce the
// same key twice for two services created in the same second.
func TestGeneratedKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	const n = 2000
	for i := 0; i < n; i++ {
		k, _ := NewGateSecret()
		if seen[k] {
			t.Fatalf("the same key was generated twice after %d draws", i)
		}
		seen[k] = true
	}
}

// A crude but effective check that the output is not obviously structured:
// every position should see many different characters across many draws,
// and the overall distribution should be near-uniform.
func TestGeneratedKeysLookRandom(t *testing.T) {
	const draws = 3000
	counts := map[rune]int{}
	firstChars := map[byte]bool{}
	for i := 0; i < draws; i++ {
		k, _ := NewGateSecret()
		firstChars[k[0]] = true
		for _, c := range k {
			counts[c]++
		}
	}
	if len(firstChars) < 40 {
		t.Errorf("only %d distinct first characters in %d keys — the output looks structured",
			len(firstChars), draws)
	}
	if len(counts) < 60 {
		t.Errorf("only %d of the 64 alphabet symbols ever appeared", len(counts))
	}

	// Shannon entropy per character, in bits. A uniform draw from 64
	// symbols is exactly 6 bits; anything below 5.9 means real bias.
	total := float64(draws * GateSecretLength)
	h := 0.0
	for _, n := range counts {
		p := float64(n) / total
		h -= p * math.Log2(p)
	}
	if h < 5.9 {
		t.Errorf("entropy is %.3f bits per character, expected ~6.0 — the draw is biased", h)
	}
	t.Logf("measured %.4f bits per character over %d keys (%.0f bits per key)",
		h, draws, h*float64(GateSecretLength))
}

// The length has to be enough that guessing is hopeless. This states the
// requirement as a number so a future "let's shorten it" is caught.
func TestKeyStrengthIsStated(t *testing.T) {
	bits := float64(GateSecretLength) * math.Log2(64)
	if bits < 128 {
		t.Errorf("a key carries only %.0f bits; brute force is feasible", bits)
	}
	t.Logf("%d characters from a 64-symbol alphabet = %.0f bits", GateSecretLength, bits)
}
