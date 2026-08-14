// Package nginx — filesystem snapshot/rollback helpers.
//
// The generator and the settings editors never modify files destructively.
// Before touching anything they take a snapshot of every file they own or
// edit; if nginx rejects the result (or the reload fails), the snapshot is
// restored so the server keeps serving the last known-good configuration.
package nginx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Backup is a snapshot of a set of files. It remembers both the content and
// whether the file existed, so Restore can recreate or remove files exactly
// as they were.
type Backup struct {
	entries map[string]backupEntry
}

type backupEntry struct {
	existed bool
	data    []byte
	mode    os.FileMode
}

// SnapBackup captures the current state of all given paths. Missing files are
// remembered as missing (restoring removes them). This never fails on a
// missing file — only on a real read error.
func SnapBackup(paths ...string) (*Backup, error) {
	b := &Backup{entries: map[string]backupEntry{}}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, dup := b.entries[p]; dup {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				b.entries[p] = backupEntry{existed: false}
				continue
			}
			return nil, fmt.Errorf("backup %s: %w", p, err)
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		b.entries[p] = backupEntry{existed: true, data: data, mode: info.Mode()}
	}
	return b, nil
}

// Restore writes every snapshot entry back to disk. It is best-effort: all
// entries are attempted and errors are joined, so a single failure does not
// stop the rest of the rollback.
func (b *Backup) Restore() error {
	if b == nil {
		return nil
	}
	var errs []error
	for p, e := range b.entries {
		if !e.existed {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove %s: %w", p, err))
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			errs = append(errs, fmt.Errorf("mkdir %s: %w", filepath.Dir(p), err))
			continue
		}
		if err := os.WriteFile(p, e.data, e.mode); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// BackupDir returns a path under /var/lib/shahrag/backups stamped with the
// current time; the installer and panel use it for human-readable backups.
func BackupDir() string {
	return filepath.Join("/var/lib/shahrag", "backups", time.Now().Format("20060102-150405"))
}
