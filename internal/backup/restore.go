package backup

// Restoring.
//
// The rule that shapes this whole file: a restore must never leave the
// server in a state that is neither the old one nor the new one. So it
// runs in three phases —
//
//	1. read and verify the archive completely, in memory, touching nothing
//	2. take a safety backup of what is about to be overwritten
//	3. write, and if anything fails, put the safety copy back
//
// Phase 1 is why the manifest carries a SHA per file: a backup that has
// been sitting on a cheap VPS for a year can be silently corrupt, and
// discovering that half way through writing config.json is the worst
// possible moment.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shahrag/internal/config"
)

// Contents is what an archive holds, read without writing anything.
type Contents struct {
	Meta  Meta
	Files map[string][]byte
}

// Inspect opens an archive and verifies it, writing nothing.
//
// This is what the panel calls to show "this backup contains 3 domains and
// 7 services, taken on Tuesday" before the operator commits to anything.
func Inspect(path, passphrase string) (*Contents, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if len(raw) >= len(magic) && string(raw[:len(magic)]) == string(magic) {
		if strings.TrimSpace(passphrase) == "" {
			return nil, fmt.Errorf("this backup is encrypted and needs its passphrase")
		}
		raw, err = decrypt(raw, passphrase)
		if err != nil {
			return nil, err
		}
	}

	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("this file is not a Shahrag backup: %w", err)
	}
	defer gz.Close()

	out := &Contents{Files: map[string][]byte{}}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the backup is damaged: %w", err)
		}
		// A tar entry naming "../.." would write outside the target on
		// extraction. Rejected rather than sanitised, because a
		// legitimate Shahrag archive never contains one and a file that
		// does is either corrupt or hostile.
		if strings.Contains(hdr.Name, "..") || strings.HasPrefix(hdr.Name, "/") {
			return nil, fmt.Errorf("the backup contains an unsafe path (%q) and was refused", hdr.Name)
		}
		// Bounded read: a crafted archive claiming a 40 GB member would
		// otherwise exhaust a 1 GB VPS before the size was noticed.
		const maxMember = 64 << 20
		data, err := io.ReadAll(io.LimitReader(tr, maxMember+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxMember {
			return nil, fmt.Errorf("%s is larger than any real backup member and was refused", hdr.Name)
		}
		out.Files[hdr.Name] = data
	}

	mb, ok := out.Files["manifest.json"]
	if !ok {
		return nil, fmt.Errorf("this archive has no manifest, so it was not made by Shahrag")
	}
	if err := json.Unmarshal(mb, &out.Meta); err != nil {
		return nil, fmt.Errorf("the manifest is unreadable: %w", err)
	}
	if out.Meta.Version > metaVersion {
		return nil, fmt.Errorf("this backup was written by a newer version of Shahrag (format %d) and cannot be read safely", out.Meta.Version)
	}

	// Verify every file against the manifest BEFORE anything is written.
	for _, f := range out.Meta.Files {
		data, ok := out.Files[f.Name]
		if !ok {
			return nil, fmt.Errorf("the backup is missing %s, which its own manifest lists", f.Name)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA {
			return nil, fmt.Errorf("%s does not match its checksum: this backup is corrupt and has NOT been restored", f.Name)
		}
	}
	if _, ok := out.Files["config.json"]; !ok {
		return nil, fmt.Errorf("the backup contains no config.json")
	}
	// The config has to parse, or restoring it would leave a panel that
	// cannot start.
	var probe config.Config
	if err := json.Unmarshal(out.Files["config.json"], &probe); err != nil {
		return nil, fmt.Errorf("the configuration inside this backup is not valid JSON: %w", err)
	}
	return out, nil
}

// RestoreOptions controls what a restore touches.
type RestoreOptions struct {
	// Config restores config.json.
	Config bool
	// Certs restores the certificate files.
	Certs bool
	// State restores the ban list and statistics.
	State bool
}

// DefaultRestore restores the configuration only.
//
// Deliberately the narrowest useful option. Certificates on disk are
// usually NEWER than the ones in a backup — certbot or the panel's own
// ACME renewed them last week — so blindly restoring a month-old
// certificate over a current one would replace something valid with
// something about to expire.
func DefaultRestore() RestoreOptions { return RestoreOptions{Config: true} }

// Result describes what a restore did.
type Result struct {
	Restored []string `json:"restored"`
	Skipped  []string `json:"skipped"`
	// SafetyCopy is where the pre-restore state was saved.
	SafetyCopy string `json:"safety_copy"`
}

// Restore writes an archive's contents back, with a rollback on failure.
func (e *Engine) Restore(path, passphrase string, opt RestoreOptions) (*Result, error) {
	c, err := Inspect(path, passphrase)
	if err != nil {
		return nil, err
	}

	cur, err := e.cfg.Read()
	if err != nil {
		return nil, err
	}

	// ── Phase 2: a safety copy of what is about to be overwritten ──
	//
	// Taken as a MANUAL backup, so the retention policy will never
	// rotate it away. Somebody restoring the wrong file and then wanting
	// to undo it is a real and panicky situation, and the escape hatch
	// must not be on a timer.
	safety, err := e.Create(config.BackupFull, GenManual)
	if err != nil {
		return nil, fmt.Errorf("refusing to restore: the safety backup of your current state failed (%w)", err)
	}

	res := &Result{SafetyCopy: safety.Path}

	// ── Phase 3: write ──
	write := func(target string, data []byte, mode os.FileMode) error {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".restore-*.tmp")
		if err != nil {
			return err
		}
		name := tmp.Name()
		defer os.Remove(name)
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chmod(name, mode); err != nil {
			return err
		}
		return os.Rename(name, target)
	}

	rollback := func(cause error) (*Result, error) {
		// Put the safety copy back. Restoring the safety copy uses the
		// same verified path, so a failure here is reported honestly
		// rather than pretending the rollback worked.
		sc, ierr := Inspect(safety.Path, cur.Backup.Passphrase)
		if ierr == nil {
			if data, ok := sc.Files["config.json"]; ok {
				_ = write(config.ConfigPath, data, 0o600)
			}
		}
		return nil, fmt.Errorf("the restore failed and your previous configuration was put back (%w). The safety copy is at %s", cause, safety.Path)
	}

	if opt.Config {
		if err := write(config.ConfigPath, c.Files["config.json"], 0o600); err != nil {
			return rollback(err)
		}
		res.Restored = append(res.Restored, "config.json")
	}

	if opt.Certs {
		for name, data := range c.Files {
			if !strings.HasPrefix(name, "certs/") {
				continue
			}
			target := "/" + strings.TrimPrefix(name, "certs/")
			// A key is 0600; a certificate is world-readable by
			// convention and nginx reads both as root anyway.
			mode := os.FileMode(0o644)
			if strings.Contains(strings.ToLower(target), "key") {
				mode = 0o600
			}
			if err := write(target, data, mode); err != nil {
				res.Skipped = append(res.Skipped, target+": "+err.Error())
				continue
			}
			res.Restored = append(res.Restored, target)
		}
	}

	if opt.State {
		for name, target := range map[string]string{
			"state/bans.json":  envOr("SHAHRAG_BANS_FILE", "/var/lib/shahrag/bans.json"),
			"state/stats.json": envOr("SHAHRAG_STATS_FILE", "/var/lib/shahrag/stats.json"),
		} {
			data, ok := c.Files[name]
			if !ok {
				continue
			}
			if err := write(target, data, 0o644); err != nil {
				res.Skipped = append(res.Skipped, target+": "+err.Error())
				continue
			}
			res.Restored = append(res.Restored, target)
		}
	}

	return res, nil
}

// Describe summarises an archive for the panel, without restoring.
func Describe(path, passphrase string) (map[string]interface{}, error) {
	c, err := Inspect(path, passphrase)
	if err != nil {
		return nil, err
	}
	files := make([]map[string]interface{}, 0, len(c.Meta.Files))
	for _, f := range c.Meta.Files {
		files = append(files, map[string]interface{}{
			"name": f.Name, "size": f.Size,
		})
	}
	return map[string]interface{}{
		"created_at": c.Meta.CreatedAt.Format(time.RFC3339),
		"scope":      c.Meta.Scope,
		"build":      c.Meta.Build,
		"hostname":   c.Meta.Hostname,
		"domains":    c.Meta.Domains,
		"services":   c.Meta.Services,
		"files":      files,
	}, nil
}
