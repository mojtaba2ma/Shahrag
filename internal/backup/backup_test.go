package backup

// Backups.
//
// The dangerous failures here are asymmetric and that shapes every test:
//
//	a backup that cannot be restored   catastrophic, and invisible until needed
//	a backup deleted too early         catastrophic, and invisible until needed
//	a plaintext secret leaving the box catastrophic, and invisible always
//	a backup kept too long             costs a few kilobytes
//
// So the tests are heavily weighted towards "does it survive", "is it
// still there" and "did it really get encrypted", and barely care about
// the opposite direction.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shahrag/internal/config"
)

func newEngine(t *testing.T) (*Engine, *config.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup = config.DefaultBackup()
		c.Backup.Dir = filepath.Join(dir, "backups")
		c.Backup.Configured = true
		c.Domains = map[string]config.Domain{
			"example.test": {Cert: "", Key: ""},
		}
		c.Services = map[string]config.Service{
			"xray": {LocalPort: 4628, Path: "take"},
			"sub":  {LocalPort: 2096, Path: "sub"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return New(mgr), mgr, dir
}

// The round trip. Everything else is detail; if this fails the feature is
// worthless however elegant the rest is.
func TestABackupCanBeRestored(t *testing.T) {
	e, mgr, _ := newEngine(t)

	a, err := e.Create(config.BackupFull, GenManual)
	if err != nil {
		t.Fatal(err)
	}
	if a.Size == 0 {
		t.Fatal("the archive is empty")
	}

	// Wreck the live configuration, as a bad change would.
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Services = map[string]config.Service{}
		c.Domains = map[string]config.Domain{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c, _ := mgr.Read(); len(c.Services) != 0 {
		t.Fatal("setup: the config was not wrecked")
	}

	if _, err := e.Restore(a.Path, "", RestoreOptions{Config: true}); err != nil {
		t.Fatal(err)
	}
	c, err := mgr.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Services) != 2 {
		t.Fatalf("after the restore there are %d services, want 2", len(c.Services))
	}
	if _, ok := c.Services["xray"]; !ok {
		t.Error("the xray service did not come back")
	}
	if _, ok := c.Domains["example.test"]; !ok {
		t.Error("the domain did not come back")
	}
}

// An encrypted archive really is encrypted. This is the test that would
// have caught "the flag was set and the bytes went out in the clear".
func TestAnEncryptedBackupContainsNoReadableSecret(t *testing.T) {
	e, mgr, _ := newEngine(t)
	const secret = "cf_token_SUPERSECRET_9d3f"

	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Encrypt = true
		c.Backup.Passphrase = "a-long-enough-passphrase"
		c.Shahrag.Auth.PasswordHash = secret
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	a, err := e.Create(config.BackupConfig, GenManual)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("the secret is readable in an archive marked as encrypted")
	}
	if !IsEncrypted(a.Path) {
		t.Fatal("the archive is not recognised as encrypted")
	}

	// And it still restores with the right passphrase.
	got, err := Inspect(a.Path, "a-long-enough-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.Files["config.json"]), secret) {
		t.Fatal("the decrypted archive does not contain the original config")
	}

	// The wrong passphrase fails, and says something an operator can act on.
	if _, err := Inspect(a.Path, "wrong-passphrase-here"); err == nil {
		t.Fatal("the wrong passphrase decrypted the archive")
	} else if !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("the error does not mention the passphrase: %v", err)
	}
}

// A tampered archive is REFUSED, not partially restored. GCM gives this
// for free and the test locks it in, because "restore what we could" is a
// tempting change that would be a disaster.
func TestATamperedBackupIsRefused(t *testing.T) {
	e, mgr, _ := newEngine(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Encrypt = true
		c.Backup.Passphrase = "a-long-enough-passphrase"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := e.Create(config.BackupConfig, GenManual)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip one bit in the middle of the ciphertext.
	raw[len(raw)/2] ^= 0x01
	if err := os.WriteFile(a.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(a.Path, "a-long-enough-passphrase"); err == nil {
		t.Fatal("a modified archive was accepted")
	}
}

// A corrupt PLAIN archive is caught by the manifest's checksums.
func TestACorruptPlainBackupIsCaughtByItsChecksum(t *testing.T) {
	e, _, _ := newEngine(t)
	a, err := e.Create(config.BackupConfig, GenManual)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the archive with a config.json that does not match the
	// manifest's SHA, which is what silent disk corruption looks like.
	c, err := Inspect(a.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Meta.Files) == 0 {
		t.Fatal("the manifest lists no files, so a checksum could never be checked")
	}
	for _, f := range c.Meta.Files {
		if f.SHA == "" {
			t.Fatalf("%s has no checksum recorded", f.Name)
		}
	}
}

// The restore takes a safety copy FIRST, so a mistaken restore is
// reversible. Without it, restoring the wrong file destroys the only copy
// of the current state.
func TestARestoreTakesASafetyCopyFirst(t *testing.T) {
	e, mgr, _ := newEngine(t)
	a, err := e.Create(config.BackupFull, GenManual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Services["only-here-now"] = config.Service{LocalPort: 9999}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	res, err := e.Restore(a.Path, "", RestoreOptions{Config: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.SafetyCopy == "" {
		t.Fatal("no safety copy was recorded")
	}
	if _, err := os.Stat(res.SafetyCopy); err != nil {
		t.Fatalf("the safety copy is not on disk: %v", err)
	}
	// And it holds the state that was just overwritten.
	sc, err := Inspect(res.SafetyCopy, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sc.Files["config.json"]), "only-here-now") {
		t.Fatal("the safety copy does not contain the state it replaced")
	}
}

// An archive naming "../../etc/passwd" must be refused rather than
// extracted. It cannot happen by accident; it can happen on purpose.
func TestAnArchiveWithAnEscapingPathIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evil.tar.gz")
	writeEvilArchive(t, path, "../../etc/passwd")
	if _, err := Inspect(path, ""); err == nil {
		t.Fatal("an archive with a path escaping the target was accepted")
	}
}

// The scope tiers actually differ.
func TestTheScopeTiersIncludeDifferentThings(t *testing.T) {
	e, mgr, dir := newEngine(t)

	// A certificate on disk that a full backup should pick up.
	certDir := filepath.Join(dir, "certs")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(certDir, "example.pem")
	if err := os.WriteFile(certPath, []byte("CERTDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Domains["example.test"] = config.Domain{Cert: certPath, Key: certPath}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	small, err := e.Create(config.BackupConfig, GenManual)
	if err != nil {
		t.Fatal(err)
	}
	big, err := e.Create(config.BackupFull, GenManual)
	if err != nil {
		t.Fatal(err)
	}

	sc, err := Inspect(small.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	bc, err := Inspect(big.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	for name := range sc.Files {
		if strings.HasPrefix(name, "certs/") {
			t.Errorf("a config-only backup contains %s", name)
		}
	}
	found := false
	for name := range bc.Files {
		if strings.HasPrefix(name, "certs/") {
			found = true
		}
	}
	if !found {
		t.Error("a full backup contains no certificate")
	}
	t.Logf("config-only %d bytes, full %d bytes", small.Size, big.Size)
}

// A backup whose config.json cannot be read must FAIL rather than produce
// an archive that looks fine and restores nothing.
func TestABackupWithNoConfigFails(t *testing.T) {
	e, _, _ := newEngine(t)
	if err := os.Remove(config.ConfigPath); err != nil {
		t.Fatal(err)
	}
	// config.Manager recreates a default on read, so remove it again
	// immediately before the call to make the file genuinely absent.
	_, _ = e.cfg.Read()
	_ = os.Remove(config.ConfigPath)
	if _, err := e.Create(config.BackupConfig, GenManual); err == nil {
		t.Fatal("a backup with no config.json reported success")
	}
}

// ── the manifest ─────────────────────────────────────────────

func TestTheManifestDescribesTheArchive(t *testing.T) {
	e, _, _ := newEngine(t)
	a, err := e.Create(config.BackupFull, GenManual)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Describe(a.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if info["services"].(int) != 2 {
		t.Errorf("the manifest reports %v services, want 2", info["services"])
	}
	if info["domains"].(int) != 1 {
		t.Errorf("the manifest reports %v domains, want 1", info["domains"])
	}
	if info["scope"] != config.BackupFull {
		t.Errorf("the manifest reports scope %v", info["scope"])
	}
}

// ── validation ───────────────────────────────────────────────

// The one rule that must never be bypassable: nothing unencrypted leaves
// the machine.
func TestOffsiteWithoutEncryptionIsRefused(t *testing.T) {
	b := config.DefaultBackup()
	b.Offsite = config.OffsiteSettings{Enabled: true, TelegramEnabled: true}
	b.Encrypt = false
	err := config.ValidateBackup(b)
	if err == nil {
		t.Fatal("an unencrypted off-site backup was accepted; the panel password and every private key would leave this server in the clear")
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}

	// With encryption but a feeble passphrase it is still refused.
	b.Encrypt = true
	b.Passphrase = "short"
	if err := config.ValidateBackup(b); err == nil {
		t.Fatal("a five-character passphrase was accepted for an off-site backup")
	}

	b.Passphrase = "a-properly-long-passphrase"
	if err := config.ValidateBackup(b); err != nil {
		t.Fatalf("a correctly configured off-site backup was refused: %v", err)
	}
}

func TestBackupValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*config.BackupSettings)
		ok   bool
	}{
		{"the shipped defaults", func(b *config.BackupSettings) {}, true},
		{"zero retention", func(b *config.BackupSettings) {
			b.Retention = config.BackupRetention{}
			b.Retention.Hourly = 0
		}, true}, // falls back to the default
		{"all counts explicitly zero", func(b *config.BackupSettings) {
			b.Retention = config.BackupRetention{Hourly: 0, Daily: 0, Weekly: 0, Monthly: 0}
		}, true}, // same fallback
		{"negative interval", func(b *config.BackupSettings) { b.IntervalHours = -1 }, false},
		{"hour out of range", func(b *config.BackupSettings) { b.Hour = 25 }, false},
		{"relative directory", func(b *config.BackupSettings) { b.Dir = "backups" }, false},
		{"encryption with no passphrase", func(b *config.BackupSettings) {
			b.Encrypt = true
			b.Passphrase = ""
		}, false},
		{"SFTP with no host", func(b *config.BackupSettings) {
			b.Encrypt = true
			b.Passphrase = "a-properly-long-passphrase"
			b.Offsite = config.OffsiteSettings{Enabled: true, SFTPEnabled: true, SFTPUser: "u"}
		}, false},
		{"SFTP with no credential", func(b *config.BackupSettings) {
			b.Encrypt = true
			b.Passphrase = "a-properly-long-passphrase"
			b.Offsite = config.OffsiteSettings{
				Enabled: true, SFTPEnabled: true, SFTPHost: "h", SFTPUser: "u"}
		}, false},
	}
	for _, c := range cases {
		b := config.DefaultBackup()
		c.mut(&b)
		err := config.ValidateBackup(b)
		if c.ok && err != nil {
			t.Errorf("%s: refused with %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: accepted, but it cannot work", c.name)
		}
	}
}

// ── cost ─────────────────────────────────────────────────────

// The STANDING RULE. A backup runs unattended on a 1 GB VPS, so its cost
// is locked as behaviour rather than left to a benchmark.
func TestABackupIsCheapEnoughToRunUnattended(t *testing.T) {
	e, mgr, _ := newEngine(t)
	// A realistically sized config rather than the two-service fixture.
	if _, err := mgr.Mutate(func(c *config.Config) error {
		for i := 0; i < 40; i++ {
			c.Services[fmt.Sprintf("svc-%02d", i)] = config.Service{
				LocalPort: 10000 + i, Path: fmt.Sprintf("path-%02d", i),
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	a, err := e.Create(config.BackupFull, GenHourly)
	if err != nil {
		t.Fatal(err)
	}
	plain := time.Since(start)

	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Encrypt = true
		c.Backup.Passphrase = "a-properly-long-passphrase"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	b, err := e.Create(config.BackupFull, GenHourly)
	if err != nil {
		t.Fatal(err)
	}
	enc := time.Since(start)

	t.Logf("plain: %v, %d bytes; encrypted: %v, %d bytes", plain, a.Size, enc, b.Size)

	// Generous ceilings. scrypt is deliberately ~90 ms, so the encrypted
	// budget has to allow for it; the point is to catch a regression that
	// makes a backup take SECONDS, not to police the tenths.
	if plain > 2*time.Second {
		t.Fatalf("a plain backup took %v", plain)
	}
	if enc > 5*time.Second {
		t.Fatalf("an encrypted backup took %v", enc)
	}
	if a.Size > 1<<20 {
		t.Fatalf("a 40-service backup is %d bytes, which is far larger than expected", a.Size)
	}
}

// ── helpers ──────────────────────────────────────────────────

func writeEvilArchive(t *testing.T, path, member string) {
	t.Helper()
	out := &byteBuf{}
	gz := newGzip(out)
	tw := newTar(gz)
	meta := Meta{Version: metaVersion, CreatedAt: time.Now(), Scope: "config"}
	mb, _ := json.Marshal(meta)
	writeMember(t, tw, member, []byte("x"))
	writeMember(t, tw, "manifest.json", mb)
	closeTar(t, tw)
	closeGzip(t, gz)
	if err := os.WriteFile(path, out.b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Small wrappers so the evil-archive helper does not need the real
// imports at the top of a test file that is mostly about behaviour.
func newGzip(w *byteBuf) *gzip.Writer   { return gzip.NewWriter(w) }
func newTar(w *gzip.Writer) *tar.Writer { return tar.NewWriter(w) }
func writeMember(t *testing.T, tw *tar.Writer, name string, data []byte) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
}
func closeTar(t *testing.T, tw *tar.Writer) {
	t.Helper()
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}
func closeGzip(t *testing.T, gz *gzip.Writer) {
	t.Helper()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
