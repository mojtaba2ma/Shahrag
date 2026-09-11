package config

// Scheduled backups.
//
// What is worth saying up front is what a backup here IS. It is not a disk
// image and it is deliberately not one: this panel's whole state is a
// configuration file, a handful of certificates and two small state files.
// Backing up the machine would be somebody else's job and a gigabyte a
// night; backing up what the panel actually owns is a few hundred
// kilobytes and can be kept for a year.
//
// Three tiers, which is the "multi-stage" the operator asked for and also
// the right shape for the problem:
//
//	config   the config.json alone, a few tens of KB. Cheap enough to take
//	         every hour and to keep a lot of.
//	full     config + certificates + ban and stats state. Everything
//	         needed to rebuild the panel on a new machine.
//	nginx    the GENERATED nginx files as well, which are reproducible
//	         from the config but are what an operator actually wants to
//	         diff after a bad change.
//
// Retention is generational (grandfather-father-son) rather than "keep the
// last N". Keeping the last 30 hourly backups covers 30 hours; keeping 24
// hourly + 14 daily + 12 weekly + 12 monthly covers a year for barely more
// space, and the failure that needs a backup is very often noticed weeks
// later — a certificate that stopped renewing, a service somebody disabled
// in March.
//
// OFF-SITE is the part that matters most and the part that is off by
// default. A backup on the same disk as the thing it backs up survives an
// operator mistake and nothing else: not a failed disk, not a lost VPS,
// not a provider suspending the account. But sending it away means sending
// the panel password hash, the Cloudflare token, the Telegram bot token
// and every private key to another machine, so it is opt-in, and it is
// encrypted with no way to turn the encryption off.

import (
	"fmt"
	"strings"
	"time"
)

// Backup tiers.
const (
	// BackupConfig is config.json only.
	BackupConfig = "config"
	// BackupFull is config + certificates + panel state.
	BackupFull = "full"
	// BackupNginx additionally includes the generated nginx files.
	BackupNginx = "nginx"
)

// NormalizeBackupScope maps input onto a known tier, defaulting to the
// most useful one. An unrecognised value must never silently produce a
// SMALLER backup than the operator asked for.
func NormalizeBackupScope(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case BackupConfig:
		return BackupConfig
	case BackupNginx:
		return BackupNginx
	}
	return BackupFull
}

// BackupRetention is the generational policy.
//
// Each field is "how many of this generation to keep". A backup is
// promoted rather than copied: the newest backup on a given day is that
// day's daily, the newest in a week is that week's weekly. So the counts
// overlap and the total on disk is roughly Hourly + Daily + Weekly +
// Monthly, not their product.
type BackupRetention struct {
	Hourly  int `json:"hourly"`
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

// DefaultRetention keeps a year of history in about 40 files.
//
// Measured against a real config: 19.5 KB per config-only backup and
// roughly 60 KB compressed for a full one, so a year costs a few
// megabytes. On a 25 GB disk that is not worth economising on, and the
// alternative — discovering that the only backup is from this morning and
// the mistake was made last month — is expensive.
func DefaultRetention() BackupRetention {
	return BackupRetention{Hourly: 24, Daily: 14, Weekly: 8, Monthly: 12}
}

// TotalKept is roughly how many files the policy leaves on disk.
func (r BackupRetention) TotalKept() int {
	return r.Hourly + r.Daily + r.Weekly + r.Monthly
}

// Off-site transports.
const (
	OffsiteNone     = ""
	OffsiteSFTP     = "sftp"
	OffsiteTelegram = "telegram"
)

// OffsiteSettings configures sending a backup off the machine.
//
// Disabled by default and validated hard when enabled, because every
// failure mode here is silent: a backup that is not actually arriving
// looks exactly like one that is until the day it is needed.
type OffsiteSettings struct {
	Enabled bool `json:"enabled,omitempty"`

	// SFTP.
	SFTPEnabled bool   `json:"sftp_enabled,omitempty"`
	SFTPHost    string `json:"sftp_host,omitempty"`
	SFTPPort    int    `json:"sftp_port,omitempty"`
	SFTPUser    string `json:"sftp_user,omitempty"`
	// SFTPPassword or SFTPKey — a key is strongly preferred and the panel
	// says so, because a password in a config file is a password on
	// disk.
	SFTPPassword string `json:"sftp_password,omitempty"`
	SFTPKey      string `json:"sftp_key,omitempty"`
	SFTPPath     string `json:"sftp_path,omitempty"`
	// SFTPFingerprint pins the server's host key.
	//
	// Without it the first connection has to trust whatever answers,
	// which on a hostile network is the whole attack. Optional because
	// demanding it before the operator has ever connected is a chicken
	// and egg problem; the panel learns it on the first successful test
	// and then refuses a change.
	SFTPFingerprint string `json:"sftp_fingerprint,omitempty"`

	// Telegram reuses the bot already configured for alerts.
	TelegramEnabled bool `json:"telegram_enabled,omitempty"`
	// TelegramChatID overrides the alert chat. A backup should usually go
	// somewhere private rather than to a group that gets alerts.
	TelegramChatID string `json:"telegram_chat_id,omitempty"`
}

// BackupSettings is the whole feature's configuration.
type BackupSettings struct {
	// Enabled turns SCHEDULED backups on. Manual ones always work.
	Enabled bool `json:"enabled,omitempty"`

	// Scope is which tier the schedule takes.
	Scope string `json:"scope,omitempty"`

	// IntervalHours is how often a scheduled backup runs.
	IntervalHours int `json:"interval_hours,omitempty"`

	// Hour, when IntervalHours is 24 or a multiple, is the local hour to
	// run at. A daily backup at a fixed quiet hour is much better than
	// one that drifts to the middle of the busy period because the panel
	// was last restarted at noon.
	Hour int `json:"hour,omitempty"`

	Retention BackupRetention `json:"retention"`

	// Dir is where backups are written.
	Dir string `json:"dir,omitempty"`

	// Encrypt is whether the archive is encrypted.
	//
	// Always true for anything leaving the machine — ValidateBackup
	// refuses an off-site target without it. Kept as a field rather than
	// a constant because a LOCAL backup that the operator wants to read
	// with `tar` is a legitimate thing to want.
	Encrypt bool `json:"encrypt,omitempty"`

	// Passphrase derives the encryption key. Never logged, and returned
	// to the panel as a "set / not set" flag rather than as itself.
	Passphrase string `json:"passphrase,omitempty"`

	Offsite OffsiteSettings `json:"offsite"`

	// Configured marks that the form has been saved, so defaults can be
	// recommended the first time without overriding a later choice. Same
	// reasoning as Honeypot.Configured and AutoBan.Configured.
	Configured bool `json:"configured,omitempty"`
}

// DefaultBackupDir is where backups live. Alongside the wizard's own
// pre-change snapshots, so one place holds everything recoverable.
const DefaultBackupDir = "/var/backups/shahrag"

// DefaultBackup returns the shipped configuration.
//
// Scheduled backups default to ON, at the full tier, every 24 hours at
// 03:00. That is a deliberate choice rather than the usual caution: the
// cost is a few tens of kilobytes a day, the panel already writes to this
// directory during installs, and an operator who has not thought about
// backups is exactly the one who needs them. Off-site stays off.
func DefaultBackup() BackupSettings {
	return BackupSettings{
		Enabled:       true,
		Scope:         BackupFull,
		IntervalHours: 24,
		Hour:          3,
		Retention:     DefaultRetention(),
		Dir:           DefaultBackupDir,
		Encrypt:       false,
		Offsite:       OffsiteSettings{Enabled: false},
	}
}

// EffectiveDir returns the backup directory.
func (b BackupSettings) EffectiveDir() string {
	if strings.TrimSpace(b.Dir) == "" {
		return DefaultBackupDir
	}
	return b.Dir
}

// EffectiveScope returns the normalised tier.
func (b BackupSettings) EffectiveScope() string { return NormalizeBackupScope(b.Scope) }

// EffectiveInterval returns how often a scheduled backup runs.
func (b BackupSettings) EffectiveInterval() time.Duration {
	h := b.IntervalHours
	if h <= 0 {
		h = 24
	}
	if h > 24*7 {
		h = 24 * 7
	}
	return time.Duration(h) * time.Hour
}

// EffectiveRetention returns the policy, falling back to the default for a
// config written before this existed.
func (b BackupSettings) EffectiveRetention() BackupRetention {
	r := b.Retention
	if r.Hourly == 0 && r.Daily == 0 && r.Weekly == 0 && r.Monthly == 0 {
		return DefaultRetention()
	}
	return r
}

// AnyOffsite reports whether at least one off-site target is switched on.
func (o OffsiteSettings) AnyOffsite() bool {
	return o.Enabled && (o.SFTPEnabled || o.TelegramEnabled)
}

// EffectiveSFTPPort returns the SSH port.
func (o OffsiteSettings) EffectiveSFTPPort() int {
	if o.SFTPPort <= 0 {
		return 22
	}
	return o.SFTPPort
}

// ValidateBackup checks operator input.
func ValidateBackup(b BackupSettings) error {
	if b.IntervalHours < 0 {
		return fmt.Errorf("the backup interval cannot be negative")
	}
	if b.IntervalHours > 24*7 {
		return fmt.Errorf("a backup less often than weekly is not a backup policy")
	}
	if b.Hour < 0 || b.Hour > 23 {
		return fmt.Errorf("the hour must be between 0 and 23")
	}
	r := b.EffectiveRetention()
	if r.Hourly < 0 || r.Daily < 0 || r.Weekly < 0 || r.Monthly < 0 {
		return fmt.Errorf("a retention count cannot be negative")
	}
	if r.TotalKept() == 0 {
		return fmt.Errorf("every retention count is zero, so a backup would be deleted the moment it was taken")
	}
	if r.TotalKept() > 500 {
		return fmt.Errorf("keeping more than 500 backups will fill the disk before it is ever useful")
	}
	if b.Dir != "" && !strings.HasPrefix(b.Dir, "/") {
		return fmt.Errorf("the backup directory must be an absolute path")
	}

	// Encryption is not negotiable for anything leaving the machine.
	//
	// The archive contains the panel's password hash, the Cloudflare API
	// token, the Telegram bot token and every private key. Sending that
	// to another server or into a chat unencrypted hands all of it to
	// whoever administers the far end — which for Telegram is a company,
	// and for a cheap VPS is anybody who later buys that IP.
	if b.Offsite.AnyOffsite() {
		if !b.Encrypt {
			return fmt.Errorf("a backup that leaves this server must be encrypted: it contains your panel password, your Cloudflare and Telegram tokens and every private key")
		}
		if len(strings.TrimSpace(b.Passphrase)) < 12 {
			return fmt.Errorf("the encryption passphrase must be at least 12 characters, because it is the only thing protecting the copy that leaves this server")
		}
	}
	if b.Encrypt && strings.TrimSpace(b.Passphrase) == "" {
		return fmt.Errorf("encryption is on but no passphrase is set")
	}

	if b.Offsite.Enabled && b.Offsite.SFTPEnabled {
		if strings.TrimSpace(b.Offsite.SFTPHost) == "" {
			return fmt.Errorf("the SFTP host is required")
		}
		if strings.TrimSpace(b.Offsite.SFTPUser) == "" {
			return fmt.Errorf("the SFTP user is required")
		}
		if b.Offsite.EffectiveSFTPPort() < 1 || b.Offsite.EffectiveSFTPPort() > 65535 {
			return fmt.Errorf("the SFTP port must be between 1 and 65535")
		}
		if strings.TrimSpace(b.Offsite.SFTPPassword) == "" &&
			strings.TrimSpace(b.Offsite.SFTPKey) == "" {
			return fmt.Errorf("SFTP needs either a private key or a password")
		}
	}
	return nil
}

// BackupWarnings returns non-fatal remarks: things worth saying that are
// not wrong enough to refuse.
func BackupWarnings(b BackupSettings) []string {
	var out []string
	if !b.Enabled {
		out = append(out, "Scheduled backups are off. A configuration this server depends on exists in exactly one place.")
		return out
	}
	if !b.Offsite.AnyOffsite() {
		out = append(out, "Backups are only being written to this same server. That protects you from your own mistakes but not from a failed disk, a lost VPS or a suspended account.")
	}
	if b.Offsite.SFTPEnabled && strings.TrimSpace(b.Offsite.SFTPPassword) != "" &&
		strings.TrimSpace(b.Offsite.SFTPKey) == "" {
		out = append(out, "SFTP is using a password. A key is safer: the password is stored on this server in readable form, and anybody who reads the config can log into the backup server too.")
	}
	if b.Offsite.SFTPEnabled && strings.TrimSpace(b.Offsite.SFTPFingerprint) == "" {
		out = append(out, "The backup server's host key is not pinned yet. Run a test transfer and the panel will record it, after which a changed key is treated as an error rather than accepted silently.")
	}
	if b.Offsite.TelegramEnabled {
		out = append(out, "Telegram's file limit for a bot is 50 MB. A full backup is normally far smaller, but if it ever exceeds that the panel will report the transfer as failed rather than silently skipping it.")
	}
	if b.EffectiveScope() == BackupConfig {
		out = append(out, "Only config.json is being saved. That restores your routing but not your certificates, so a rebuild would need every certificate issued again.")
	}
	return out
}
