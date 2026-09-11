package web

// The backup API.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shahrag/internal/backup"
	"shahrag/internal/config"
)

type backupResp struct {
	config.BackupSettings
	// Restated without omitempty so a stored false is reported as false
	// rather than omitted — the same bug that made the ban-history
	// checkbox unrenderable in r49.
	Enabled bool `json:"enabled"`
	Encrypt bool `json:"encrypt"`
	// PassphraseSet reports WHETHER a passphrase exists, never what it
	// is. A secret that round-trips through the browser is a secret in
	// the browser's memory, in its cache and in any proxy log on the way.
	PassphraseSet bool `json:"passphrase_set"`
	// Same for the SFTP credentials.
	SFTPPasswordSet bool `json:"sftp_password_set"`

	Defaults config.BackupSettings `json:"defaults"`
	Warnings []string              `json:"warnings,omitempty"`

	Archives []backup.Archive `json:"archives"`
	// TotalBytes is what the backups currently occupy.
	TotalBytes int64     `json:"total_bytes"`
	LastBackup time.Time `json:"last_backup"`
	// NextBackup is when the schedule will next fire, so the operator can
	// see that it is actually going to happen.
	NextBackup *time.Time `json:"next_backup,omitempty"`
	Running    bool       `json:"running"`
}

func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	b := c.Backup
	// A config written before this feature existed has a zero
	// BackupSettings, which would render as a disabled form with every
	// field blank. Show the recommended policy instead — the operator has
	// never seen this screen, so their silence is not a decision.
	if !b.Configured && b.IntervalHours == 0 {
		d := config.DefaultBackup()
		d.Configured = false
		b = d
	}

	resp := backupResp{
		BackupSettings:  b,
		Enabled:         b.Enabled,
		Encrypt:         b.Encrypt,
		PassphraseSet:   strings.TrimSpace(b.Passphrase) != "",
		SFTPPasswordSet: strings.TrimSpace(b.Offsite.SFTPPassword) != "",
		Defaults:        config.DefaultBackup(),
		Warnings:        config.BackupWarnings(b),
		LastBackup:      c.Shahrag.LastBackup,
		Running:         s.backups != nil,
	}
	// Never send the secrets themselves.
	resp.BackupSettings.Passphrase = ""
	resp.BackupSettings.Offsite.SFTPPassword = ""
	resp.BackupSettings.Offsite.SFTPKey = b.Offsite.SFTPKey // a PATH, not a key

	if s.backups != nil {
		if list, err := s.backups.List(); err == nil {
			resp.Archives = list
			for _, a := range list {
				resp.TotalBytes += a.Size
			}
		}
	}
	if resp.Archives == nil {
		resp.Archives = []backup.Archive{}
	}
	if b.Enabled {
		next := nextRun(b, c.Shahrag.LastBackup, time.Now())
		resp.NextBackup = &next
	}
	writeJSON(w, 200, resp)
}

// nextRun works out when the schedule fires next.
//
// Computed by asking Due() minute by minute rather than by arithmetic on
// the interval. That is a little crude and it is deliberate: the schedule's
// truth is Due(), and a second implementation of the same rule here is how
// the displayed time and the actual time drift apart.
func nextRun(b config.BackupSettings, last, now time.Time) time.Time {
	t := now.Truncate(time.Minute)
	for i := 0; i < 8*24*60; i++ {
		if backup.Due(b, last, t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

type backupReq struct {
	Enabled       *bool   `json:"enabled"`
	Scope         *string `json:"scope"`
	IntervalHours *int    `json:"interval_hours"`
	Hour          *int    `json:"hour"`
	Dir           *string `json:"dir"`
	Encrypt       *bool   `json:"encrypt"`
	// Passphrase is only applied when non-empty: the form sends an empty
	// string when the operator has not retyped it, and treating that as
	// "clear the passphrase" would silently disable encryption on the
	// next run.
	Passphrase *string                 `json:"passphrase"`
	Retention  *config.BackupRetention `json:"retention"`
	Offsite    *offsiteReq             `json:"offsite"`
}

type offsiteReq struct {
	Enabled         *bool   `json:"enabled"`
	SFTPEnabled     *bool   `json:"sftp_enabled"`
	SFTPHost        *string `json:"sftp_host"`
	SFTPPort        *int    `json:"sftp_port"`
	SFTPUser        *string `json:"sftp_user"`
	SFTPPassword    *string `json:"sftp_password"`
	SFTPKey         *string `json:"sftp_key"`
	SFTPPath        *string `json:"sftp_path"`
	SFTPFingerprint *string `json:"sftp_fingerprint"`
	TelegramEnabled *bool   `json:"telegram_enabled"`
	TelegramChatID  *string `json:"telegram_chat_id"`
}

func (s *Server) handleSetBackup(w http.ResponseWriter, r *http.Request) {
	var body backupReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		b := c.Backup
		if !b.Configured && b.IntervalHours == 0 {
			b = config.DefaultBackup()
		}
		if body.Enabled != nil {
			b.Enabled = *body.Enabled
		}
		if body.Scope != nil {
			b.Scope = config.NormalizeBackupScope(*body.Scope)
		}
		if body.IntervalHours != nil {
			b.IntervalHours = *body.IntervalHours
		}
		if body.Hour != nil {
			b.Hour = *body.Hour
		}
		if body.Dir != nil && strings.TrimSpace(*body.Dir) != "" {
			b.Dir = strings.TrimSpace(*body.Dir)
		}
		if body.Encrypt != nil {
			b.Encrypt = *body.Encrypt
		}
		if body.Passphrase != nil && strings.TrimSpace(*body.Passphrase) != "" {
			b.Passphrase = *body.Passphrase
		}
		if body.Retention != nil {
			b.Retention = *body.Retention
		}
		if o := body.Offsite; o != nil {
			if o.Enabled != nil {
				b.Offsite.Enabled = *o.Enabled
			}
			if o.SFTPEnabled != nil {
				b.Offsite.SFTPEnabled = *o.SFTPEnabled
			}
			if o.SFTPHost != nil {
				b.Offsite.SFTPHost = strings.TrimSpace(*o.SFTPHost)
			}
			if o.SFTPPort != nil {
				b.Offsite.SFTPPort = *o.SFTPPort
			}
			if o.SFTPUser != nil {
				b.Offsite.SFTPUser = strings.TrimSpace(*o.SFTPUser)
			}
			if o.SFTPPassword != nil && strings.TrimSpace(*o.SFTPPassword) != "" {
				b.Offsite.SFTPPassword = *o.SFTPPassword
			}
			if o.SFTPKey != nil {
				b.Offsite.SFTPKey = strings.TrimSpace(*o.SFTPKey)
			}
			if o.SFTPPath != nil {
				b.Offsite.SFTPPath = strings.TrimSpace(*o.SFTPPath)
			}
			if o.SFTPFingerprint != nil {
				b.Offsite.SFTPFingerprint = strings.TrimSpace(*o.SFTPFingerprint)
			}
			if o.TelegramEnabled != nil {
				b.Offsite.TelegramEnabled = *o.TelegramEnabled
			}
			if o.TelegramChatID != nil {
				b.Offsite.TelegramChatID = strings.TrimSpace(*o.TelegramChatID)
			}
		}
		b.Configured = true
		if err := config.ValidateBackup(b); err != nil {
			return err
		}
		c.Backup = b
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c, _ := s.cfg.Read()
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "warnings": config.BackupWarnings(c.Backup),
	})
}

// handleRunBackup takes one now.
func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeErr(w, 503, "the backup engine is not running")
		return
	}
	var body struct {
		Scope string `json:"scope"`
	}
	_ = readJSON(r, &body)
	c, _ := s.cfg.Read()
	scope := body.Scope
	if scope == "" {
		scope = c.Backup.EffectiveScope()
	}
	// Labelled manual, which the retention policy never prunes: somebody
	// pressing this button before a risky change must still have the file
	// afterwards.
	a, err := s.backupSched.RunNow(scope, backup.GenManual)
	if err != nil && a == nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := map[string]interface{}{"archive": a}
	if err != nil {
		// The local backup worked and the off-site copy did not. Saying
		// "failed" would be wrong and saying "ok" would hide it.
		out["offsite_error"] = err.Error()
	}
	writeJSON(w, 200, out)
}

// handleListBackups returns the archives.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeJSON(w, 200, map[string]interface{}{"archives": []interface{}{}})
		return
	}
	list, err := s.backups.List()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"archives": list})
}

// resolveArchive turns a name into a path inside the backup directory.
//
// The name is taken as a BASENAME only. Without that, "../../etc/shadow"
// would be a valid archive name and this endpoint would happily read,
// describe and delete arbitrary files as root.
func (s *Server) resolveArchive(name string) (string, error) {
	c, err := s.cfg.Read()
	if err != nil {
		return "", err
	}
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == "/" || base == "" || strings.Contains(name, "..") {
		return "", os.ErrNotExist
	}
	full := filepath.Join(c.Backup.EffectiveDir(), base)
	if _, err := os.Stat(full); err != nil {
		return "", os.ErrNotExist
	}
	return full, nil
}

// handleInspectBackup describes an archive without restoring it.
func (s *Server) handleInspectBackup(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveArchive(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "no such backup")
		return
	}
	c, _ := s.cfg.Read()
	info, err := backup.Describe(path, c.Backup.Passphrase)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, info)
}

// handleRestoreBackup puts one back.
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeErr(w, 503, "the backup engine is not running")
		return
	}
	path, err := s.resolveArchive(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "no such backup")
		return
	}
	var body struct {
		Config     bool   `json:"config"`
		Certs      bool   `json:"certs"`
		State      bool   `json:"state"`
		Passphrase string `json:"passphrase"`
	}
	_ = readJSON(r, &body)

	c, _ := s.cfg.Read()
	pass := body.Passphrase
	if strings.TrimSpace(pass) == "" {
		pass = c.Backup.Passphrase
	}

	opt := backup.RestoreOptions{Config: body.Config, Certs: body.Certs, State: body.State}
	if !opt.Config && !opt.Certs && !opt.State {
		opt = backup.DefaultRestore()
	}

	res, err := s.backups.Restore(path, pass, opt)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// nginx is regenerated from the restored config, and the result is
	// reported: a restore that produces a config nginx rejects has to say
	// so rather than leaving the operator to find out at the next reload.
	out := map[string]interface{}{"result": res}
	for k, v := range applied(s.autoApply()) {
		out[k] = v
	}
	writeJSON(w, 200, out)
}

// handleDeleteBackup removes one.
func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveArchive(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "no such backup")
		return
	}
	if err := os.Remove(path); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"deleted": filepath.Base(path)})
}

// handleDownloadBackup streams an archive to the browser.
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveArchive(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "no such backup")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer f.Close()
	name := filepath.Base(path)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, time.Time{}, f)
}

// handleTestOffsite performs a REAL transfer.
//
// A test that only validates the form is worse than no test: it reports
// success for settings whose actual failure will be the host key, the
// remote path or the permissions.
func (s *Server) handleTestOffsite(w http.ResponseWriter, r *http.Request) {
	if s.backupSender == nil {
		writeErr(w, 503, "the backup engine is not running")
		return
	}
	if err := s.backupSender.TestSFTP(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c, _ := s.cfg.Read()
	writeJSON(w, 200, map[string]interface{}{
		"ok": true,
		// The pinned key, so the panel can show that it now has one.
		"fingerprint": c.Backup.Offsite.SFTPFingerprint != "",
	})
}
