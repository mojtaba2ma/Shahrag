package cli

// Certificate management from the terminal.
//
// Same engine as the web panel, same guarantee: an issued certificate is
// written into the domain's config entry and nginx is regenerated, so it is
// actually served rather than just sitting on disk.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"shahrag/internal/certs"
	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

func menuCerts(cfg *config.Manager, gen *nginxpkg.Generator, in *bufio.Reader) {
	for {
		c, err := cfg.Read()
		if err != nil {
			fmt.Println(red(err.Error()))
			pause(in)
			return
		}
		fmt.Println("\n── Certificates ──")

		names := make([]string, 0, len(c.Domains))
		for n := range c.Domains {
			names = append(names, n)
		}
		sort.Strings(names)

		if len(names) == 0 {
			fmt.Println("  no domains yet — add one from the Domains menu first")
		}
		for i, n := range names {
			d := c.Domains[n]
			info := certs.Inspect(n, d.Cert, d.Key)
			state := ""
			switch {
			case info.Error != "":
				state = red(info.Error)
			case info.Expired:
				state = red("EXPIRED")
			case info.DueRenew:
				state = yellow(fmt.Sprintf("renew soon (%dd)", info.DaysLeft))
			case info.SelfSuper:
				state = yellow("self-signed")
			default:
				state = green(fmt.Sprintf("%dd left", info.DaysLeft))
			}
			extra := ""
			if info.Wildcard {
				extra += " " + green("[wildcard]")
			}
			if d.ACME != nil && d.ACME.Managed {
				extra += " [managed]"
			}
			if d.ACME != nil && d.ACME.Staging {
				extra += " " + yellow("[staging]")
			}
			fmt.Printf("  %d) %-28s %s%s\n", i+1, n, state, extra)
			if len(info.Names) > 0 {
				fmt.Printf("      covers: %s\n", strings.Join(info.Names, ", "))
			}
		}

		acme := c.Shahrag.ACME
		cf := red("not set")
		if strings.TrimSpace(acme.CloudflareToken) != "" {
			cf = green("configured")
		}
		fmt.Printf("\n  Cloudflare token: %s   email: %s   staging: %v   auto-renew: %v\n",
			cf, orDash(acme.Email), acme.Staging, acme.AutoRenew)

		fmt.Println("\n  i) Issue / renew   s) Issuance settings   d) Detach   0) Back")
		fmt.Print("Choose: ")
		switch strings.TrimSpace(mustRead(in)) {
		case "i":
			issueFromCLI(cfg, gen, in, names)
		case "s":
			acmeSettings(cfg, in)
		case "d":
			detachCert(cfg, in, names)
		case "0", "":
			return
		}
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func acmeSettings(cfg *config.Manager, in *bufio.Reader) {
	c, _ := cfg.Read()
	a := c.Shahrag.ACME

	fmt.Printf("\nEmail for expiry warnings [%s]: ", orDash(a.Email))
	if v := strings.TrimSpace(mustRead(in)); v != "" {
		a.Email = v
	}

	fmt.Println("\nCloudflare API token — needs ONLY the Zone:DNS:Edit permission.")
	fmt.Println("Do not use a Global API Key: it would give the panel your whole account.")
	fmt.Print("Token (blank keeps the current one): ")
	if v := strings.TrimSpace(mustRead(in)); v != "" {
		fmt.Print("  verifying... ")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := certs.NewCloudflareProvider(v).VerifyToken(ctx)
		cancel()
		if err != nil {
			fmt.Println(red("rejected: " + err.Error()))
			pause(in)
			return
		}
		fmt.Println(green("ok"))
		a.CloudflareToken = v
	}

	a.AutoRenew = askYes(in, "Renew automatically 30 days before expiry?", a.AutoRenew)
	a.Staging = askYes(in, "Use the STAGING CA (untrusted certs, loose rate limits)?", a.Staging)

	if _, err := cfg.Mutate(func(c *config.Config) error {
		c.Shahrag.ACME = a
		return nil
	}); err != nil {
		fmt.Println(red(err.Error()))
	} else {
		fmt.Println(green("saved"))
	}
	pause(in)
}

func issueFromCLI(cfg *config.Manager, gen *nginxpkg.Generator, in *bufio.Reader, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Print("Domain number: ")
	idx, err := strconv.Atoi(strings.TrimSpace(mustRead(in)))
	if err != nil || idx < 1 || idx > len(names) {
		fmt.Println(red("invalid number"))
		pause(in)
		return
	}
	domain := names[idx-1]

	c, _ := cfg.Read()
	// Wildcard is the default: it covers the bare domain AND every
	// subdomain in one certificate, which is what a panel deployment
	// almost always wants.
	wildcard := askYes(in, fmt.Sprintf("Wildcard (%s + *.%s)?", domain, domain), true)

	// Resolve the defaults for this domain: its own override first, then
	// the account-wide value.
	dcur := c.Domains[domain]
	email := c.Shahrag.ACME.Email
	token := c.Shahrag.ACME.CloudflareToken
	if dcur.ACME != nil {
		if dcur.ACME.Email != "" {
			email = dcur.ACME.Email
		}
		if dcur.ACME.CloudflareToken != "" {
			token = dcur.ACME.CloudflareToken
		}
	}

	// Both are per-domain: several domains can sit in different Cloudflare
	// accounts, so the stored values are only a starting point.
	fmt.Printf("Email for this domain [%s]: ", orDash(email))
	if v := strings.TrimSpace(mustRead(in)); v != "" {
		email = v
	}

	method := "manual"
	if token != "" {
		fmt.Print("Cloudflare token (blank = use the stored one): ")
		if v := strings.TrimSpace(mustRead(in)); v != "" {
			token = v
		}
		if askYes(in, "Use Cloudflare (automatic)?", true) {
			method = "cloudflare"
		}
	} else {
		fmt.Print("Cloudflare token (blank = manual DNS instead): ")
		if v := strings.TrimSpace(mustRead(in)); v != "" {
			token = v
			method = "cloudflare"
		} else {
			fmt.Println(yellow("  no token — using the manual DNS flow"))
		}
	}

	var provider certs.DNSProvider
	if method == "cloudflare" {
		provider = certs.NewCloudflareProvider(token)
	} else {
		provider = &certs.ManualProvider{
			Instruct: func(name, value string) error {
				fmt.Println("\n" + yellow("Create this DNS record, then press Enter:"))
				fmt.Printf("    Type : TXT\n")
				fmt.Printf("    Name : %s\n", name)
				fmt.Printf("    Value: %s\n", value)
				fmt.Print("\nPress Enter once the record exists... ")
				_ = mustRead(in)
				return nil
			},
			Cleanup: func(name, value string) {
				fmt.Printf("  (you may now delete the TXT record %s)\n", name)
			},
		}
	}

	iss := &certs.Issuer{
		DNS:      provider,
		Log:      func(f string, a ...interface{}) { fmt.Printf("  "+f+"\n", a...) },
		HTTPRoot: c.Nginx.FakeDir,
	}

	fmt.Println("\nRequesting the certificate. DNS propagation can take a minute or two.")
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()

	res, err := iss.Issue(ctx, certs.Request{
		Domain:    domain,
		Wildcard:  wildcard,
		Challenge: certs.ChallengeDNS,
		Email:     email,
		Staging:   c.Shahrag.ACME.Staging,
	})
	if err != nil {
		fmt.Println(red("failed: " + err.Error()))
		pause(in)
		return
	}

	// Install it, or the certificate exists but nothing uses it.
	if _, err := cfg.Mutate(func(cc *config.Config) error {
		d, ok := cc.Domains[domain]
		if !ok {
			return fmt.Errorf("domain %s disappeared", domain)
		}
		d.Cert, d.Key = res.CertPath, res.KeyPath
		meta := &config.CertMeta{
			Managed: true, Wildcard: wildcard,
			Challenge: certs.ChallengeDNS,
			Issued:    time.Now().Format(time.RFC3339),
			Staging:   c.Shahrag.ACME.Staging,
		}
		// Remember an override only when it differs from the account-wide
		// default, so a later change in Settings still propagates.
		if email != c.Shahrag.ACME.Email {
			meta.Email = email
		}
		if token != c.Shahrag.ACME.CloudflareToken {
			meta.CloudflareToken = token
		}
		d.ACME = meta
		cc.Domains[domain] = d
		return nil
	}); err != nil {
		fmt.Println(red("issued but could not save: " + err.Error()))
		pause(in)
		return
	}

	fmt.Println(green(fmt.Sprintf("\ncertificate installed for %s", domain)))
	fmt.Printf("  covers : %s\n", strings.Join(res.Names, ", "))
	fmt.Printf("  expires: %s\n", res.NotAfter.Format("2006-01-02"))

	if gen != nil {
		fmt.Print("  regenerating nginx... ")
		if _, err := gen.GenerateAndReload(); err != nil {
			fmt.Println(red("failed: " + err.Error()))
			fmt.Println("  the certificate is saved; fix the error and run Generate")
		} else {
			fmt.Println(green("done"))
		}
	}
	pause(in)
}

func detachCert(cfg *config.Manager, in *bufio.Reader, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Print("Domain number: ")
	idx, err := strconv.Atoi(strings.TrimSpace(mustRead(in)))
	if err != nil || idx < 1 || idx > len(names) {
		fmt.Println(red("invalid number"))
		pause(in)
		return
	}
	domain := names[idx-1]
	if !askYes(in, fmt.Sprintf("Detach the certificate from %s? (files stay on disk)", domain), false) {
		return
	}
	if _, err := cfg.Mutate(func(c *config.Config) error {
		d := c.Domains[domain]
		d.Cert, d.Key, d.ACME = "", "", nil
		c.Domains[domain] = d
		return nil
	}); err != nil {
		fmt.Println(red(err.Error()))
	} else {
		fmt.Println(green("detached"))
	}
	pause(in)
}

// RunRenew is the entry point for the systemd timer. It renews every managed
// certificate that is inside the renewal window and reloads nginx once, at
// the end, only if something actually changed.
func RunRenew() int {
	cfg := config.New()
	c, err := cfg.Read()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read config:", err)
		return 1
	}
	if !c.Shahrag.ACME.AutoRenew {
		fmt.Println("automatic renewal is disabled")
		return 0
	}

	names := make([]string, 0, len(c.Domains))
	for n := range c.Domains {
		names = append(names, n)
	}
	sort.Strings(names)

	changed := false
	failed := 0
	for _, n := range names {
		d := c.Domains[n]
		if d.ACME == nil || !d.ACME.Managed {
			continue // never touch a certificate the operator installed
		}
		info := certs.Inspect(n, d.Cert, d.Key)
		if !info.DueRenew && info.Error == "" {
			continue
		}
		fmt.Printf("renewing %s (%d days left)\n", n, info.DaysLeft)

		// A domain may live in a different Cloudflare account, so its own
		// token wins over the account-wide one.
		tok := strings.TrimSpace(c.Shahrag.ACME.CloudflareToken)
		email := c.Shahrag.ACME.Email
		if d.ACME.CloudflareToken != "" {
			tok = strings.TrimSpace(d.ACME.CloudflareToken)
		}
		if d.ACME.Email != "" {
			email = d.ACME.Email
		}

		var provider certs.DNSProvider
		if tok != "" {
			provider = certs.NewCloudflareProvider(tok)
		} else {
			// Unattended renewal cannot ask a human for a TXT record.
			fmt.Printf("  skipped: %s needs the manual DNS flow, which cannot run unattended\n", n)
			failed++
			continue
		}

		iss := &certs.Issuer{
			DNS: provider,
			Log: func(f string, a ...interface{}) { fmt.Printf("  "+f+"\n", a...) },
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		res, err := iss.Issue(ctx, certs.Request{
			Domain: n, Wildcard: d.ACME.Wildcard,
			Challenge: certs.ChallengeDNS,
			Email:     email,
			Staging:   d.ACME.Staging,
		})
		cancel()
		if err != nil {
			fmt.Printf("  %s failed: %v\n", n, err)
			failed++
			continue
		}
		if _, err := cfg.Mutate(func(cc *config.Config) error {
			dd := cc.Domains[n]
			dd.Cert, dd.Key = res.CertPath, res.KeyPath
			dd.ACME.Issued = time.Now().Format(time.RFC3339)
			cc.Domains[n] = dd
			return nil
		}); err != nil {
			fmt.Printf("  %s renewed but not saved: %v\n", n, err)
			failed++
			continue
		}
		changed = true
	}

	if changed {
		gen := nginxpkg.NewGenerator(cfg)
		if _, err := gen.GenerateAndReload(); err != nil {
			fmt.Println("nginx reload failed:", err)
			return 1
		}
		fmt.Println("nginx reloaded")
	} else {
		fmt.Println("nothing to renew")
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// ── Automatic renewal timer ──────────────────────────────────

// SystemdDir is where the renewal units are written. A variable so tests can
// point it somewhere harmless.
var SystemdDir = envOr("SHAHRAG_SYSTEMD_DIR", "/etc/systemd/system")

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// InstallRenewTimer writes a systemd timer that renews certificates twice a
// day.
//
// Twice daily is what every ACME client does and what Let's Encrypt asks
// for: renewal starts 30 days before expiry, so there are ~60 chances to
// succeed before anything breaks. RandomizedDelaySec spreads the load so the
// whole world does not hit the CA on the hour.
func InstallRenewTimer(binary string) error {
	if binary == "" {
		binary = "/usr/local/bin/shahrag"
	}
	service := "[Unit]\n" +
		"Description=Shahrag certificate renewal\n" +
		"After=network-online.target\n" +
		"Wants=network-online.target\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=" + binary + " renew-certs\n"

	timer := "[Unit]\n" +
		"Description=Renew Shahrag certificates twice a day\n\n" +
		"[Timer]\n" +
		"OnCalendar=*-*-* 03,15:00:00\n" +
		"RandomizedDelaySec=3600\n" +
		"Persistent=true\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"

	if err := os.MkdirAll(SystemdDir, 0o755); err != nil {
		return err
	}
	sp := filepath.Join(SystemdDir, "shahrag-renew.service")
	tp := filepath.Join(SystemdDir, "shahrag-renew.timer")
	if err := os.WriteFile(sp, []byte(service), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(tp, []byte(timer), 0o644); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = exec.Command("systemctl", "enable", "--now", "shahrag-renew.timer").Run()
	return nil
}
