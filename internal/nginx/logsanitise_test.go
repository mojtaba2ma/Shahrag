package nginx

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// nginx writes the RAW BYTES of what it failed to parse into error.log. The
// operator photographed a page full of  and empty squares and reported it
// as a font problem; it is not one. No font has a glyph for a byte that is
// not a character, and encoding/json turns every invalid byte into U+FFFD
// before the browser ever sees it.
func TestBrokenHeaderBytesBecomeReadable(t *testing.T) {
	// A real line, byte for byte: the start of a TLS ClientHello that
	// arrived on a port expecting a PROXY header.
	raw := "2026/09/17 06:12:26 [error] 2822582#2822582: *9411496 broken header: " +
		"\"\x16\x03\x01]\x9aM\\&\xaa\x07@<t\x00\x1b;\" while reading PROXY protocol, " +
		"client: 85.217.149.28, server: 0.0.0.0:6038"

	if utf8.ValidString(raw) {
		t.Fatal("fixture is wrong: the raw line should not be valid UTF-8")
	}

	out := SanitiseLog(raw)

	if !utf8.ValidString(out) {
		t.Error("the sanitised line is still not valid UTF-8, so JSON will " +
			"replace bytes with U+FFFD and the operator still sees squares")
	}
	if strings.ContainsRune(out, '\uFFFD') {
		t.Error("the sanitised line contains a replacement character")
	}
	// The interesting part must be READABLE, not merely safe: \x16\x03\x01
	// is a TLS 1.0 record header and that is the whole diagnosis.
	if !strings.Contains(out, `\x16\x03\x01`) {
		t.Errorf("the TLS record header is not legible in the output:\n%s", out)
	}
	// And the human half of the line must survive untouched.
	for _, want := range []string{
		"2026/09/17 06:12:26", "[error]", "broken header:",
		"while reading PROXY protocol", "client: 85.217.149.28",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the readable part lost %q", want)
		}
	}
	t.Logf("in : %d bytes, valid UTF-8 = %v", len(raw), utf8.ValidString(raw))
	t.Logf("out: %s", out)
}

// Real UTF-8 in a log — a Persian server_name, a UTF-8 path — must survive
// exactly. Escaping it would be a different bug with the same symptom.
func TestRealUTF8InLogsIsUntouched(t *testing.T) {
	for _, s := range []string{
		`2026/09/17 06:12:26 [error] server: "سایت.ir" request: "GET /مسیر HTTP/1.1"`,
		"plain ascii line with tabs\tand a newline\nsecond line",
		`host: "例え.jp"`,
	} {
		if got := SanitiseLog(s); got != s {
			t.Errorf("valid text was altered:\n in: %q\nout: %q", s, got)
		}
	}
}

// Control characters would break the page layout even when they are valid
// UTF-8 — a bare \r or an ANSI escape from a misbehaving upstream.
func TestControlCharactersAreEscaped(t *testing.T) {
	out := SanitiseLog("before\x1b[31mred\x07\x00after")
	for _, bad := range []string{"\x1b", "\x07", "\x00"} {
		if strings.Contains(out, bad) {
			t.Errorf("control byte %q survived: %q", bad, out)
		}
	}
	if !strings.Contains(out, `\x1b`) || !strings.Contains(out, `\x00`) {
		t.Errorf("control bytes were dropped instead of shown: %q", out)
	}
	// Tabs and newlines are legitimate structure and must be kept.
	if got := SanitiseLog("a\tb\nc"); got != "a\tb\nc" {
		t.Errorf("tab or newline was escaped: %q", got)
	}
}

// The logs page polls; this runs over up to 1000 lines every time. A clean
// line must cost a scan and no allocation.
func TestSanitiseIsFreeForCleanLines(t *testing.T) {
	clean := strings.Repeat(
		`10.0.0.1 - - [21/Sep/2026:20:00:00 +0000] "GET /x HTTP/1.1" 200 512`+"\n", 1000)
	if got := SanitiseLog(clean); got != clean {
		t.Fatal("a clean block was modified")
	}
	n := testing.AllocsPerRun(50, func() { SanitiseLog(clean) })
	t.Logf("allocations for 1000 clean lines: %.0f", n)
	if n > 0 {
		t.Errorf("the fast path allocates %.0f times; it should return the input unchanged", n)
	}
}
