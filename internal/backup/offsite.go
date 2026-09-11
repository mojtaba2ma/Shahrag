package backup

// Getting the backup off the machine.
//
// Two transports, both deliberately built on things already present rather
// than on new dependencies:
//
//	SFTP      shells out to the system's own sftp/scp client
//	Telegram  posts to the Bot API with net/http
//
// Shelling out to sftp instead of vendoring an SSH library is a real
// trade-off, so here is the reasoning. golang.org/x/crypto/ssh plus
// pkg/sftp is roughly 2 MB of vendored code and a permanent maintenance
// surface, to do something OpenSSH already does on every server this runs
// on — and does with the operator's existing ~/.ssh config, their agent,
// their jump hosts and their hardware keys, none of which a library would
// pick up. The cost is one fork per backup, which against a once-a-day
// job is nothing.
//
// The part that is NOT delegated is host key checking. StrictHostKeyChecking
// is forced on and the panel pins the fingerprint itself, because the
// default "ask" behaviour has no terminal to ask on and would silently
// accept whatever answers.

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"shahrag/internal/config"
)

// Sender copies a finished archive off the machine.
type Sender struct {
	cfg *config.Manager
}

// NewSender builds one.
func NewSender(cfg *config.Manager) *Sender { return &Sender{cfg: cfg} }

// Send copies an archive to every enabled target.
//
// Every target is attempted even if an earlier one failed, and all the
// errors are reported together. Stopping at the first failure would mean a
// broken SFTP server silently disabling the Telegram copy that was working
// perfectly well.
func (s *Sender) Send(a *Archive) error {
	c, err := s.cfg.Read()
	if err != nil {
		return err
	}
	o := c.Backup.Offsite
	if !o.AnyOffsite() {
		return nil
	}

	// Refuse to send anything unencrypted, whatever the settings say.
	//
	// ValidateBackup already enforces this when the form is saved, but
	// this is the last gate before bytes leave the machine and it is
	// cheap. A config edited by hand, or a field added later that
	// forgets the check, must not be able to put a plaintext password
	// hash on somebody else's disk.
	if !IsEncrypted(a.Path) {
		return fmt.Errorf("refusing to send an unencrypted backup off this server: it contains the panel password, API tokens and private keys")
	}

	var errs []string
	if o.SFTPEnabled {
		if err := s.sendSFTP(c, a); err != nil {
			errs = append(errs, "SFTP: "+err.Error())
		}
	}
	if o.TelegramEnabled {
		if err := s.sendTelegram(c, a); err != nil {
			errs = append(errs, "Telegram: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// sftpBinary finds the client.
func sftpBinary() string {
	for _, n := range []string{"sftp", "scp"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// sendSFTP uploads with the system client.
func (s *Sender) sendSFTP(c *config.Config, a *Archive) error {
	o := c.Backup.Offsite
	bin := sftpBinary()
	if bin == "" {
		return fmt.Errorf("no sftp or scp client is installed on this server (apt install openssh-client)")
	}

	// A known_hosts file of our own, holding exactly the pinned key.
	//
	// Not the root user's: this must not be able to authorise a host for
	// anything else on the box, and it must not be affected by whatever
	// is already in there.
	dir := filepath.Join(c.Backup.EffectiveDir(), ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	known := filepath.Join(dir, "known_hosts")

	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=20",
		"-o", "UserKnownHostsFile=" + known,
	}
	if strings.TrimSpace(o.SFTPFingerprint) == "" {
		// First contact: learn the key and record it. Trust-on-first-use
		// is weaker than a pinned key and it is stated as such in the
		// panel, but it is strictly better than the alternative of
		// accepting a different key on every connection for ever.
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	} else {
		args = append(args, "-o", "StrictHostKeyChecking=yes")
		if err := os.WriteFile(known,
			[]byte(strings.TrimSpace(o.SFTPFingerprint)+"\n"), 0o600); err != nil {
			return err
		}
	}

	if k := strings.TrimSpace(o.SFTPKey); k != "" {
		args = append(args, "-o", "IdentitiesOnly=yes", "-i", k)
	} else if strings.TrimSpace(o.SFTPPassword) != "" {
		// BatchMode=yes disables the password prompt, so a password
		// needs sshpass. Reported as a clear instruction rather than as
		// a mystery timeout.
		if _, err := exec.LookPath("sshpass"); err != nil {
			return fmt.Errorf("a password needs the sshpass tool (apt install sshpass), or use a private key instead, which is safer anyway")
		}
	}

	remote := strings.TrimSpace(o.SFTPPath)
	if remote == "" {
		remote = "."
	}
	target := fmt.Sprintf("%s@%s:%s/%s", o.SFTPUser, o.SFTPHost,
		strings.TrimSuffix(remote, "/"), a.Name)

	var cmd *exec.Cmd
	if strings.HasSuffix(bin, "scp") {
		full := append([]string{"-P", strconv.Itoa(o.EffectiveSFTPPort())}, args...)
		full = append(full, a.Path, target)
		cmd = exec.Command(bin, full...)
	} else {
		full := append([]string{"-P", strconv.Itoa(o.EffectiveSFTPPort())}, args...)
		// sftp takes its commands on stdin in batch mode.
		full = append(full, "-b", "-", fmt.Sprintf("%s@%s", o.SFTPUser, o.SFTPHost))
		cmd = exec.Command(bin, full...)
		cmd.Stdin = strings.NewReader(fmt.Sprintf("put %q %q\nbye\n",
			a.Path, strings.TrimSuffix(remote, "/")+"/"+a.Name))
	}
	if strings.TrimSpace(o.SFTPKey) == "" && strings.TrimSpace(o.SFTPPassword) != "" {
		// SSHPASS in the environment, never on the command line, where
		// it would be visible in `ps` to every user on the machine.
		cmd = exec.Command("sshpass", append([]string{"-e", cmd.Path}, cmd.Args[1:]...)...)
		cmd.Env = append(os.Environ(), "SSHPASS="+o.SFTPPassword)
	}

	// A hung transfer must not hold the scheduler for ever.
	done := make(chan error, 1)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			msg := strings.TrimSpace(out.String())
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("%s", firstLine(msg))
		}
	case <-time.After(10 * time.Minute):
		_ = cmd.Process.Kill()
		return fmt.Errorf("the transfer timed out after ten minutes")
	}

	// Record the key we learned, so the next run pins it.
	if strings.TrimSpace(o.SFTPFingerprint) == "" {
		if kb, err := os.ReadFile(known); err == nil && len(kb) > 0 {
			_, _ = s.cfg.Mutate(func(cc *config.Config) error {
				cc.Backup.Offsite.SFTPFingerprint = strings.TrimSpace(string(kb))
				return nil
			})
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// telegramMaxUpload is the Bot API's limit for a bot-sent document.
const telegramMaxUpload = 50 << 20

// sendTelegram posts the archive to the bot.
func (s *Sender) sendTelegram(c *config.Config, a *Archive) error {
	token := strings.TrimSpace(c.Telegram.Token)
	if token == "" {
		return fmt.Errorf("no Telegram bot token is configured")
	}
	chat := strings.TrimSpace(c.Backup.Offsite.TelegramChatID)
	if chat == "" {
		if len(c.Telegram.ChatIDs) == 0 {
			return fmt.Errorf("no Telegram chat is configured to send the backup to")
		}
		// The alert chats are numeric IDs; the override field is a
		// string so an operator can also paste a channel name like
		// "@my_backups", which the Bot API accepts in the same field.
		chat = strconv.FormatInt(c.Telegram.ChatIDs[0], 10)
	}

	fi, err := os.Stat(a.Path)
	if err != nil {
		return err
	}
	if fi.Size() > telegramMaxUpload {
		// Reported as a failure rather than skipped quietly: an operator
		// relying on this copy has to find out.
		return fmt.Errorf("the backup is %.1f MB and Telegram's limit for a bot is 50 MB",
			float64(fi.Size())/(1<<20))
	}

	f, err := os.Open(a.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", chat)
	_ = mw.WriteField("caption", fmt.Sprintf("Shahrag backup\n%s\nscope: %s\nsize: %.0f KB",
		a.CreatedAt.Format("2006-01-02 15:04"), a.Scope, float64(a.Size)/1024))
	part, err := mw.CreateFormFile("document", a.Name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	api := telegramAPI
	if api == "" {
		api = "https://api.telegram.org"
	}
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/bot%s/sendDocument", api, token), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	cl := &http.Client{Timeout: 5 * time.Minute}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return fmt.Errorf("Telegram refused the upload (%d): %s",
			resp.StatusCode, firstLine(strings.TrimSpace(string(rb))))
	}
	return nil
}

// telegramAPI is overridable so a test can point at a local stub instead
// of the real Bot API.
var telegramAPI = os.Getenv("SHAHRAG_TELEGRAM_API")

// TestSFTP verifies the SFTP settings by sending a tiny probe file.
//
// A "test" button that only checks the form fields is worse than none: it
// tells the operator their settings are fine when the thing that will
// actually fail is the host key, the path or the permissions. This does
// the real transfer.
func (s *Sender) TestSFTP() error {
	c, err := s.cfg.Read()
	if err != nil {
		return err
	}
	if !c.Backup.Offsite.SFTPEnabled {
		return fmt.Errorf("SFTP is not enabled")
	}
	dir := c.Backup.EffectiveDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".shahrag-connection-test")
	if err := os.WriteFile(probe,
		[]byte("Shahrag connection test "+time.Now().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(probe)
	return s.sendSFTP(c, &Archive{
		Name: ".shahrag-connection-test", Path: probe,
		CreatedAt: time.Now(), Scope: "test",
	})
}
