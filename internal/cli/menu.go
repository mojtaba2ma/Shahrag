// Package cli implements the interactive terminal menu for Shahrag.
package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/installer"
	nginxpkg "shahrag/internal/nginx"
	"shahrag/internal/security"
	"shahrag/internal/systemd"
)

const version = "1.0.0"

// RunMenu is the main entry point for the interactive menu.
func RunMenu() int {
	cfg := config.New()
	gen := nginxpkg.NewGenerator(cfg)
	in := bufio.NewReader(os.Stdin)

	for {
		clearScreen()
		printBanner(cfg)
		fmt.Println("  1) Services            2) Domains")
		fmt.Println("  3) Listen ports        4) SNI routing")
		fmt.Println("  5) Fake site           6) Nginx settings")
		fmt.Println("  7) Panel settings      8) Admin password")
		fmt.Println("  9) Generate & reload  10) Backup / restore")
		fmt.Println(" 11) Web panel service  12) View status")
		fmt.Println(" 13) Doctor            14) Logs")
		fmt.Println(" 15) Config files")
		fmt.Println("  0) Exit")
		fmt.Print("\nChoose: ")
		line, _ := in.ReadString('\n')
		switch strings.TrimSpace(line) {
		case "1":
			menuServices(cfg, in)
		case "2":
			menuDomains(cfg, in)
		case "3":
			menuPorts(cfg, in)
		case "4":
			menuReality(cfg, in)
		case "5":
			menuFakeSite(cfg, in)
		case "6":
			menuNginx(cfg, in)
		case "7":
			menuPanel(cfg, in)
		case "8":
			menuPassword(cfg, in)
		case "9":
			generate(gen)
			pause(in)
		case "10":
			menuBackup(cfg, in)
		case "11":
			menuWeb(in)
		case "12":
			printStatus(cfg, gen)
			pause(in)
		case "13":
			RunDoctor()
			pause(in)
		case "14":
			menuLogs(in)
		case "15":
			menuFiles(cfg, in)
		case "0", "q", "exit":
			fmt.Println("Bye.")
			return 0
		}
	}
}

func clearScreen() { fmt.Print("\033[H\033[2J") }

func printBanner(cfg *config.Manager) {
	c, _ := cfg.Read()
	p := c.Shahrag.Panel
	state := red("not installed")
	if p.Installed {
		if p.Domain != "" {
			state = green(fmt.Sprintf("https://%s.%s/%s/", p.Subdomain, p.Domain, p.Path))
		} else {
			state = green(fmt.Sprintf("http://SERVER_IP:%d/%s/", p.LocalPort, p.Path))
		}
	}
	fmt.Printf(`
  ╔════════════════════════════════════════════════════╗
  ║  Shahrag v%-5s   nginx control panel             ║
  ║  Panel: %-43s║
  ╚════════════════════════════════════════════════════╝
`, version, state)
}

// ── Services ───────────────────────────────────────────────

func menuServices(cfg *config.Manager, in *bufio.Reader) {
	for {
		c, _ := cfg.Read()
		fmt.Println("\n── Services ──")
		names := make([]string, 0, len(c.Services))
		i := 1
		for name := range c.Services {
			names = append(names, name)
			s := c.Services[name]
			binds := []string{}
			for _, b := range s.Bindings {
				f := b.Domain
				if b.Subdomain != "" {
					f = b.Subdomain + "." + b.Domain
				}
				binds = append(binds, f)
			}
			extra := ""
			if name == c.Shahrag.Panel.ServiceName {
				extra = yellow(" [panel]")
			}
			// Surface the bot shield here too: an operator working from the
			// terminal must be able to see which services are protected
			// without opening the GUI.
			switch config.NormalizeGate(s.Gate) {
			case config.GateJS:
				extra += green(" [shield:browser]")
			case config.GateSecret:
				extra += green(" [shield:key]")
			}
			fmt.Printf("  %d) %-18s :%d→:%d /%-12s %s%s\n", i, name, s.LocalPort, s.ListenPort, pathStr(s.Path), strings.Join(binds, ","), extra)
			i++
		}
		if len(names) == 0 {
			fmt.Println("  (none)")
		}
		fmt.Println("\n  a) Add   d) Delete   e) Edit bindings   0) Back")
		switch strings.TrimSpace(mustRead(in)) {
		case "a":
			addService(cfg, in)
		case "d":
			deleteService(cfg, in, names)
		case "e":
			editBindings(cfg, in)
		case "0", "b", "":
			return
		}
	}
}

func addService(cfg *config.Manager, in *bufio.Reader) {
	c, _ := cfg.Read()
	fmt.Print("Name: ")
	name := strings.TrimSpace(mustRead(in))
	if name == "" || name == c.Shahrag.Panel.ServiceName {
		return
	}
	if _, ok := c.Services[name]; ok {
		fmt.Println(red("Already exists."))
		pause(in)
		return
	}
	fmt.Print("Subdomain: ")
	sub := strings.TrimSpace(mustRead(in))
	fmt.Print("Domain: ")
	domain := strings.TrimSpace(mustRead(in))
	if _, ok := c.Domains[domain]; !ok {
		fmt.Println(red("Domain not found — add it first."))
		pause(in)
		return
	}
	lp := readInt(in, "Local port: ", 8080)
	lip := readInt(in, "Listen port [443]: ", 443)
	fmt.Print("Path (enter for root): ")
	pth := strings.Trim(strings.TrimSpace(mustRead(in)), "/")
	if pth == "" {
		pth = "/"
	}
	owned := askYes(in, "Panel core path (path_owned)?", pth != "/")
	ssl := askYes(in, "SSL backend?", false)

	// Bot shield. Default no, so the CLI behaves exactly as it always did
	// unless the operator asks for the protection.
	gate, gateSecret := "", ""
	if askYes(in, "Hide this service behind a bot shield?", false) {
		fmt.Println("    1) browser check (automatic, no key to remember)")
		fmt.Println("    2) access key (visitors must type a word)")
		fmt.Print("  Choice [1]: ")
		switch strings.TrimSpace(mustRead(in)) {
		case "2":
			for {
				fmt.Print("  Access key (4-64 chars, letters/digits/-/_): ")
				k := strings.TrimSpace(mustRead(in))
				if nginxpkg.ValidGateSecret(k) {
					gate, gateSecret = config.GateSecret, k
					break
				}
				fmt.Println(red("  Invalid key."))
			}
		default:
			tok, err := nginxpkg.NewGateToken()
			if err != nil {
				fmt.Println(red("could not generate a token: " + err.Error()))
				pause(in)
				return
			}
			gate, gateSecret = config.GateJS, tok
		}
	}

	if err := cfg.AddService(name, sub, domain, lp, lip, pth, owned, ssl); err != nil {
		fmt.Println(red(err.Error()))
		pause(in)
		return
	}
	if gate != "" {
		if _, err := cfg.Mutate(func(c *config.Config) error {
			svc := c.Services[name]
			svc.Gate, svc.GateSecret = gate, gateSecret
			c.Services[name] = svc
			return nil
		}); err != nil {
			fmt.Println(red("service added but the shield could not be stored: " + err.Error()))
			pause(in)
		}
	}
}

func deleteService(cfg *config.Manager, in *bufio.Reader, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Print("Number: ")
	idx, err := strconv.Atoi(strings.TrimSpace(mustRead(in)))
	if err != nil || idx < 1 || idx > len(names) {
		return
	}
	c, _ := cfg.Read()
	if names[idx-1] == c.Shahrag.Panel.ServiceName {
		fmt.Println(red("Cannot delete panel service; use Panel settings."))
		pause(in)
		return
	}
	_ = cfg.DeleteService(names[idx-1])
}

func editBindings(cfg *config.Manager, in *bufio.Reader) {
	fmt.Println("  a) Add   d) Delete   0) Back")
	switch strings.TrimSpace(mustRead(in)) {
	case "a":
		fmt.Print("Service: ")
		name := strings.TrimSpace(mustRead(in))
		fmt.Print("Subdomain: ")
		sub := strings.TrimSpace(mustRead(in))
		fmt.Print("Domain: ")
		domain := strings.TrimSpace(mustRead(in))
		if err := cfg.AddBinding(name, sub, domain); err != nil {
			fmt.Println(red(err.Error()))
			pause(in)
		}
	case "d":
		fmt.Print("Service: ")
		name := strings.TrimSpace(mustRead(in))
		c, _ := cfg.Read()
		svc, ok := c.Services[name]
		if !ok {
			return
		}
		for i, b := range svc.Bindings {
			fmt.Printf("  %d) %s.%s\n", i+1, b.Subdomain, b.Domain)
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(mustRead(in)))
		_ = cfg.RemoveBinding(name, idx-1)
	}
}

// ── Domains ────────────────────────────────────────────────

func menuDomains(cfg *config.Manager, in *bufio.Reader) {
	for {
		c, _ := cfg.Read()
		fmt.Println("\n── Domains ──")
		names := make([]string, 0, len(c.Domains))
		for n := range c.Domains {
			names = append(names, n)
		}
		for i, n := range names {
			d := c.Domains[n]
			cert := d.Cert
			if cert == "" {
				cert = yellow("(no cert)")
			}
			fmt.Printf("  %d) %-30s %s\n", i+1, n, cert)
		}
		fmt.Println("\n  a) Add   d) Delete   e) Edit cert/key   0) Back")
		switch strings.TrimSpace(mustRead(in)) {
		case "a":
			fmt.Print("Domain: ")
			name := strings.TrimSpace(mustRead(in))
			fmt.Print("Cert: ")
			cert := strings.TrimSpace(mustRead(in))
			fmt.Print("Key: ")
			key := strings.TrimSpace(mustRead(in))
			if err := cfg.AddDomain(name, cert, key); err != nil {
				fmt.Println(red(err.Error()))
				pause(in)
			}
		case "d":
			fmt.Print("Number: ")
			idx, err := strconv.Atoi(strings.TrimSpace(mustRead(in)))
			if err == nil && idx >= 1 && idx <= len(names) {
				_ = cfg.DeleteDomain(names[idx-1])
			}
		case "e":
			fmt.Print("Domain: ")
			name := strings.TrimSpace(mustRead(in))
			fmt.Print("Cert: ")
			cert := strings.TrimSpace(mustRead(in))
			fmt.Print("Key: ")
			key := strings.TrimSpace(mustRead(in))
			_, _ = cfg.Mutate(func(c *config.Config) error {
				if d, ok := c.Domains[name]; ok {
					d.Cert = cert
					d.Key = key
					c.Domains[name] = d
				}
				return nil
			})
		case "0", "b", "":
			return
		}
	}
}

// ── Ports ──────────────────────────────────────────────────

func menuPorts(cfg *config.Manager, in *bufio.Reader) {
	for {
		c, _ := cfg.Read()
		fmt.Println("\n── Listen ports ──")
		for _, p := range c.ListenPorts {
			tag := "HTTPS"
			if p == 80 {
				tag = "HTTP redirect"
			}
			fmt.Printf("  %d  [%s]\n", p, tag)
		}
		fmt.Println("\n  a) Add   d) Delete   0) Back")
		switch strings.TrimSpace(mustRead(in)) {
		case "a":
			_ = cfg.AddPort(readInt(in, "Port: ", 443))
		case "d":
			_ = cfg.DeletePort(readInt(in, "Port: ", 0))
		case "0", "b", "":
			return
		}
	}
}

// ── Reality ────────────────────────────────────────────────

func menuReality(cfg *config.Manager, in *bufio.Reader) {
	for {
		c, _ := cfg.Read()
		fmt.Printf("\n── Reality ──\n  Enabled: %v   HTTP port: %d\n", c.Reality.Enabled, c.Reality.HTTPPort)
		i := 1
		for name, s := range c.Reality.Services {
			fmt.Printf("  %d) %-15s sni=%s local=:%d ports=%v\n", i, name, s.SNI, s.LocalPort, s.Ports)
			i++
		}
		fmt.Println("\n  t) Toggle   p) HTTP port   a) Add   d) Delete   0) Back")
		switch strings.TrimSpace(mustRead(in)) {
		case "t":
			_, _ = cfg.Mutate(func(c *config.Config) error { c.Reality.Enabled = !c.Reality.Enabled; return nil })
		case "p":
			p := readInt(in, "HTTP port: ", c.Reality.HTTPPort)
			_, _ = cfg.Mutate(func(c *config.Config) error { c.Reality.HTTPPort = p; return nil })
		case "a":
			fmt.Print("Name: ")
			name := strings.TrimSpace(mustRead(in))
			fmt.Print("SNI: ")
			sni := strings.TrimSpace(mustRead(in))
			lp := readInt(in, "Local port: ", 443)
			_, _ = cfg.Mutate(func(c *config.Config) error {
				c.Reality.Services[name] = config.RealityService{SNI: sni, LocalPort: lp, Ports: []int{443}}
				return nil
			})
		case "d":
			fmt.Print("Name: ")
			name := strings.TrimSpace(mustRead(in))
			_, _ = cfg.Mutate(func(c *config.Config) error { delete(c.Reality.Services, name); return nil })
		case "0", "b", "":
			return
		}
	}
}

// ── Fake site ──────────────────────────────────────────────

func menuFakeSite(cfg *config.Manager, in *bufio.Reader) {
	c, _ := cfg.Read()
	fmt.Printf("\n── Fake site ──\n  Mode: %s\n", c.FakeSite.Mode)
	fmt.Println("  1) default   2) custom HTML   3) external file   0) Back")
	switch strings.TrimSpace(mustRead(in)) {
	case "1":
		_, _ = cfg.Mutate(func(c *config.Config) error { c.FakeSite = config.FakeSite{Mode: "default"}; return nil })
	case "2":
		fmt.Println("HTML (empty line to finish):")
		var lines []string
		for {
			l, _ := in.ReadString('\n')
			l = strings.TrimRight(l, "\n")
			if l == "" {
				break
			}
			lines = append(lines, l)
		}
		_, _ = cfg.Mutate(func(c *config.Config) error {
			c.FakeSite = config.FakeSite{Mode: "custom_content", Content: strings.Join(lines, "\n")}
			return nil
		})
	case "3":
		fmt.Print("File path: ")
		p := strings.TrimSpace(mustRead(in))
		_, _ = cfg.Mutate(func(c *config.Config) error {
			c.FakeSite = config.FakeSite{Mode: "custom_file", SourcePath: p}
			return nil
		})
	}
}

// ── Nginx settings ─────────────────────────────────────────

func menuNginx(cfg *config.Manager, in *bufio.Reader) {
	for {
		curCache := nginxpkg.CacheEnabled()
		curWC := nginxpkg.WorkerConnections()
		curLog := nginxpkg.LogLevel()
		fmt.Println("\n── Nginx ──")
		fmt.Printf("  1) Cache enabled: %v\n  2) worker_connections: %d\n  3) log level: %s\n", curCache, curWC, curLog)
		// Boot readiness is shown here because "nginx is gone after a
		// reboot" is only visible once it is too late otherwise.
		if nginxpkg.IsEnabled() && nginxpkg.DropInInstalled() {
			fmt.Printf("  boot: %s\n", green("protected (enabled + auto-restart drop-in)"))
		} else {
			fmt.Printf("  boot: %s  → 7) Fix boot protection\n",
				red("NOT protected — nginx may stay down after a reboot"))
		}
		if !nginxpkg.RLimitSatisfied(nginxpkg.WorkerRLimit(), curWC) {
			fmt.Printf("  %s worker_rlimit_nofile (%d) is below worker_connections (%d) → option 7 fixes it\n",
				red("warning:"), nginxpkg.WorkerRLimit(), curWC)
		} else if curWC > nginxpkg.MaxWorkerRLimit {
			fmt.Printf("  note: worker_connections %d exceeds the %d fd ceiling — effective limit is %d\n",
				curWC, nginxpkg.MaxWorkerRLimit, nginxpkg.MaxWorkerRLimit)
		}
		fmt.Println("  4) Reload   5) Test config   6) Save values   7) Fix boot protection   0) Back")
		switch strings.TrimSpace(mustRead(in)) {
		case "1":
			_ = nginxpkg.SetCache(!curCache)
		case "2":
			v := readInt(in, "Value: ", curWC)
			if err := nginxpkg.SetWorkerConnections(v); err != nil {
				fmt.Println(red("Error: " + err.Error()))
			} else if err := nginxpkg.EnsureWorkerRLimit(v); err != nil {
				fmt.Println(red("worker_rlimit_nofile: " + err.Error()))
			}
		case "3":
			fmt.Print("Level: ")
			_ = nginxpkg.SetLogLevel(strings.TrimSpace(mustRead(in)))
		case "4":
			// Reload a running nginx, start a stopped one (see Reload).
			r := nginxpkg.NewGenerator(cfg).Reload()
			if r.OK {
				fmt.Println(green("nginx is running with the current config."))
			} else {
				fmt.Println(red("reload/start failed:"))
				fmt.Println(r.Stderr)
			}
			pause(in)
		case "5":
			t := nginxpkg.NewGenerator(cfg).Test()
			fmt.Println(t.Stdout, t.Stderr)
			// "conflicting server name ... ignored" means nginx DROPPED a
			// server block: the services in it silently serve the fake
			// page. Regenerating with the current binary fixes it.
			if strings.Contains(t.Stderr, "conflicting server name") {
				fmt.Println(red("nginx ignored duplicate server blocks — some services are unreachable."))
				fmt.Println("Run option 'Generate' in the main menu (or: shahrag generate) to rewrite the config.")
			}
			pause(in)
		case "6":
			_, _ = cfg.Mutate(func(c *config.Config) error {
				c.NginxSettings.CacheEnabled = nginxpkg.CacheEnabled()
				c.NginxSettings.WorkerConnections = nginxpkg.WorkerConnections()
				return nil
			})
		case "7":
			for _, a := range nginxpkg.BootGuard(nginxpkg.NewGenerator(cfg)) {
				fmt.Println("  • " + a)
			}
			if wc := nginxpkg.WorkerConnections(); wc > 0 {
				if err := nginxpkg.EnsureWorkerRLimit(wc); err != nil {
					fmt.Println(red("worker_rlimit_nofile: " + err.Error()))
				}
			}
			fmt.Println(green("Boot protection applied."))
			pause(in)
		case "0", "b", "":
			return
		}
	}
}

// ── Panel settings ────────────────────────────────────────

func menuPanel(cfg *config.Manager, in *bufio.Reader) {
	c, _ := cfg.Read()
	p := c.Shahrag.Panel
	fmt.Println("\n── Panel settings ──")
	fmt.Printf("  Domain:     %s\n  Subdomain:  %s\n  Local port: %d\n  Listen port: %d\n  Path:       %s\n  Cert:       %s\n  Key:        %s\n",
		p.Domain, p.Subdomain, p.LocalPort, p.ListenPort, p.Path, p.Cert, p.Key)
	fmt.Println("\n  1) Domain/subdomain   2) Local port")
	fmt.Println("  3) Listen port       4) Path")
	fmt.Println("  5) Cert/key          6) Random path")
	fmt.Println("  0) Back")
	switch strings.TrimSpace(mustRead(in)) {
	case "1":
		fmt.Print("Domain: ")
		d := strings.TrimSpace(mustRead(in))
		fmt.Print("Subdomain: ")
		sub := strings.TrimSpace(mustRead(in))
		_, _ = cfg.Mutate(func(c *config.Config) error {
			c.Shahrag.Panel.Domain = d
			c.Shahrag.Panel.Subdomain = sub
			svc := c.Services[c.Shahrag.Panel.ServiceName]
			if len(svc.Bindings) > 0 {
				svc.Bindings[0].Domain = d
				svc.Bindings[0].Subdomain = sub
			} else if d != "" {
				svc.Bindings = []config.Binding{{Domain: d, Subdomain: sub}}
			}
			c.Services[c.Shahrag.Panel.ServiceName] = svc
			if d != "" {
				if _, ok := c.Domains[d]; !ok {
					c.Domains[d] = config.Domain{}
				}
			}
			return nil
		})
	case "2":
		n := readInt(in, "New local port: ", p.LocalPort)
		if n < 1 || n > 65535 {
			fmt.Println(red("Invalid port."))
			pause(in)
			return
		}
		// The running panel holds the current port; changing to a different
		// one must be free. The probe ignores the port we are on ourselves.
		if n != p.LocalPort && !installer.PortFree("0.0.0.0", n) {
			fmt.Println(red("Port is already in use by another process."))
			pause(in)
			return
		}
		_, _ = cfg.Mutate(func(c *config.Config) error {
			c.Shahrag.Panel.LocalPort = n
			svc := c.Services[c.Shahrag.Panel.ServiceName]
			svc.LocalPort = n
			c.Services[c.Shahrag.Panel.ServiceName] = svc
			return nil
		})
		if n != p.LocalPort {
			fmt.Println(yellow("Restarting the web panel to bind the new port..."))
			_ = systemd.Restart(systemd.UnitName)
		}
	case "3":
		n := readInt(in, "New listen port: ", p.ListenPort)
		if n < 1 || n > 65535 {
			fmt.Println(red("Invalid port."))
			pause(in)
			return
		}
		_, _ = cfg.Mutate(func(c *config.Config) error {
			c.Shahrag.Panel.ListenPort = n
			svc := c.Services[c.Shahrag.Panel.ServiceName]
			svc.ListenPort = n
			c.Services[c.Shahrag.Panel.ServiceName] = svc
			// The generator only emits server blocks for ports listed in
			// ListenPorts; keep the two in sync.
			if !containsIntC(c.ListenPorts, n) {
				c.ListenPorts = append(c.ListenPorts, n)
			}
			return nil
		})
	case "4":
		fmt.Print("New path (enter for random): ")
		np := strings.Trim(strings.TrimSpace(mustRead(in)), "/")
		if np == "" {
			np = config.RandomPath(22)
		}
		_, _ = cfg.Mutate(func(c *config.Config) error {
			c.Shahrag.Panel.Path = np
			svc := c.Services[c.Shahrag.Panel.ServiceName]
			svc.Path = np
			svc.PathOwned = true
			c.Services[c.Shahrag.Panel.ServiceName] = svc
			return nil
		})
	case "5":
		fmt.Print("Cert: ")
		cert := strings.TrimSpace(mustRead(in))
		fmt.Print("Key: ")
		key := strings.TrimSpace(mustRead(in))
		_, _ = cfg.Mutate(func(c *config.Config) error {
			c.Shahrag.Panel.Cert = cert
			c.Shahrag.Panel.Key = key
			dom := c.Shahrag.Panel.Domain
			if dom != "" {
				d := c.Domains[dom]
				d.Cert = cert
				d.Key = key
				c.Domains[dom] = d
			}
			return nil
		})
	case "6":
		np := config.RandomPath(22)
		_, _ = cfg.Mutate(func(c *config.Config) error {
			c.Shahrag.Panel.Path = np
			svc := c.Services[c.Shahrag.Panel.ServiceName]
			svc.Path = np
			c.Services[c.Shahrag.Panel.ServiceName] = svc
			return nil
		})
		fmt.Println("New path:", np)
		pause(in)
	}
}

// ── Password ──────────────────────────────────────────────

func menuPassword(cfg *config.Manager, in *bufio.Reader) {
	fmt.Print("Current password: ")
	old, err := readPassword()
	fmt.Println()
	if err != nil {
		fmt.Println(red(err.Error()))
		pause(in)
		return
	}
	c, _ := cfg.Read()
	if !security.VerifyPassword(old, c.Shahrag.Auth.PasswordHash) {
		fmt.Println(red("Incorrect."))
		pause(in)
		return
	}
	fmt.Print("New password (min 6): ")
	np, err := readPassword()
	fmt.Println()
	if err != nil || len(np) < 6 {
		fmt.Println(red("Too short or unreadable."))
		pause(in)
		return
	}
	_, _ = cfg.Mutate(func(c *config.Config) error {
		c.Shahrag.Auth.PasswordHash = security.HashPassword(np)
		return nil
	})
	fmt.Println(green("Updated."))
	pause(in)
}

// readPassword reads a password from /dev/tty with echo disabled.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	old, err := getTerminalState(fd)
	if err != nil {
		// Fallback to plain read
		in := bufio.NewReader(os.Stdin)
		s, _ := in.ReadString('\n')
		return strings.TrimRight(s, "\n"), nil
	}
	defer restoreTerminal(fd, old)
	return readLineDisabled()
}

// ── Backup ────────────────────────────────────────────────

// DefaultBackupDir is where the panel keeps its own backups. Exports default
// here so a later restore can simply list what is available.
const DefaultBackupDir = "/var/backups/shahrag"

func menuBackup(cfg *config.Manager, in *bufio.Reader) {
	fmt.Println("\n── Backup ──\n  1) Export   2) Import   0) Back")
	switch strings.TrimSpace(mustRead(in)) {
	case "1":
		// Show the default location and accept Enter for it. Asking for a
		// bare "Output path:" with no hint meant guessing where the file
		// should go, and a typo silently wrote it somewhere unfindable.
		_ = os.MkdirAll(DefaultBackupDir, 0o700)
		def := filepath.Join(DefaultBackupDir,
			fmt.Sprintf("config-%s.json", time.Now().Format("20060102-150405")))
		fmt.Printf("Default: %s\n", def)
		fmt.Print("Output path (Enter for default): ")
		p := strings.TrimSpace(mustRead(in))
		if p == "" {
			p = def
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			fmt.Println(red(err.Error()))
			pause(in)
			return
		}
		c, err := cfg.Read()
		if err != nil {
			fmt.Println(red(err.Error()))
			pause(in)
			return
		}
		data, _ := json.MarshalIndent(c, "", "  ")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			fmt.Println(red(err.Error()))
			pause(in)
			return
		}
		fmt.Println(green("Saved: " + p))
		pause(in)

	case "2":
		// List what is actually in the default directory and let the
		// operator pick by number, while still accepting a full path.
		files := listBackups(DefaultBackupDir)
		if len(files) > 0 {
			fmt.Printf("\nBackups in %s:\n", DefaultBackupDir)
			for i, f := range files {
				info := ""
				if st, err := os.Stat(f); err == nil {
					info = fmt.Sprintf("  (%s, %.1f KB)",
						st.ModTime().Format("2006-01-02 15:04"), float64(st.Size())/1024)
				}
				fmt.Printf("  %2d) %s%s\n", i+1, filepath.Base(f), info)
			}
			fmt.Println()
			fmt.Print("Number, or a full path: ")
		} else {
			fmt.Printf("\nNo backups found in %s\n", DefaultBackupDir)
			fmt.Print("Input path: ")
		}
		choice := strings.TrimSpace(mustRead(in))
		if choice == "" {
			return
		}
		p := choice
		if n, err := strconv.Atoi(choice); err == nil {
			if n < 1 || n > len(files) {
				fmt.Println(red("No such number."))
				pause(in)
				return
			}
			p = files[n-1]
		}
		data, err := os.ReadFile(p)
		if err != nil {
			fmt.Println(red(err.Error()))
			pause(in)
			return
		}
		var c config.Config
		if err := json.Unmarshal(data, &c); err != nil {
			fmt.Println(red("Not a valid config file: " + err.Error()))
			pause(in)
			return
		}
		if c.Domains == nil || c.Services == nil {
			fmt.Println(red("That file does not look like a Shahrag config."))
			pause(in)
			return
		}
		// Snapshot the CURRENT config first: a restore is destructive and
		// the operator may have picked the wrong file.
		if cur, err := cfg.Read(); err == nil {
			_ = os.MkdirAll(DefaultBackupDir, 0o700)
			safety := filepath.Join(DefaultBackupDir,
				fmt.Sprintf("pre-restore-%s.json", time.Now().Format("20060102-150405")))
			if b, err := json.MarshalIndent(cur, "", "  "); err == nil {
				if os.WriteFile(safety, b, 0o600) == nil {
					fmt.Println("Current config saved to: " + safety)
				}
			}
		}
		if err := cfg.Write(&c); err != nil {
			fmt.Println(red(err.Error()))
			pause(in)
			return
		}
		fmt.Println(green("Restored: " + p))
		fmt.Println("Run 'Generate' to apply it to nginx.")
		pause(in)
	}
}

// listBackups returns the *.json files in dir, newest first.
func listBackups(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type item struct {
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.path)
	}
	return out
}

// ── Logs ─────────────────────────────────────────────────

// menuLogs shows the nginx and panel logs from the CLI, so a broken panel can
// still be diagnosed over SSH without knowing the log paths by heart.
func menuLogs(in *bufio.Reader) {
	for {
		fmt.Println("\n── Logs ──")
		fmt.Println("  1) nginx error log")
		fmt.Println("  2) nginx access log")
		fmt.Println("  3) nginx stream log")
		fmt.Println("  4) Shahrag panel service (journalctl)")
		fmt.Println("  5) Install log")
		fmt.Println("  0) Back")
		choice := strings.TrimSpace(mustRead(in))
		if choice == "0" || choice == "" {
			return
		}
		lines := readInt(in, "How many lines? ", 50)
		switch choice {
		case "1":
			showFileLog("/var/log/nginx/error.log", lines)
		case "2":
			showFileLog("/var/log/nginx/access.log", lines)
		case "3":
			showFileLog("/var/log/nginx/stream.log", lines)
		case "4":
			showJournal("shahrag", lines)
		case "5":
			showFileLog("/var/log/shahrag-install.log", lines)
		default:
			continue
		}
		pause(in)
	}
}

func showFileLog(path string, lines int) {
	fmt.Printf("\n── %s (last %d lines) ──\n", path, lines)
	out := nginxpkg.TailLog(path, lines)
	if strings.TrimSpace(out) == "" {
		fmt.Println("  (empty)")
		return
	}
	fmt.Println(out)
}

func showJournal(unit string, lines int) {
	fmt.Printf("\n── journalctl -u %s (last %d lines) ──\n", unit, lines)
	out, err := exec.Command("journalctl", "-u", unit, "--no-pager",
		"-n", strconv.Itoa(lines)).CombinedOutput()
	if err != nil && len(out) == 0 {
		fmt.Println(red("cannot read the journal: " + err.Error()))
		return
	}
	fmt.Println(strings.TrimSpace(string(out)))
}

// ── Web service ──────────────────────────────────────────

func menuWeb(in *bufio.Reader) {
	fmt.Println("\n── Web panel ──\n  1) Start   2) Stop   3) Restart   4) Status   5) Enable   0) Back")
	act := ""
	switch strings.TrimSpace(mustRead(in)) {
	case "1":
		act = "start"
	case "2":
		act = "stop"
	case "3":
		act = "restart"
	case "4":
		act = "status"
	case "5":
		act = "enable"
	default:
		return
	}
	cmd := exec.Command("systemctl", act, "shahrag")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	if act != "status" {
		pause(in)
	}
}

// ── Generate & status ────────────────────────────────────

// generate runs GenerateAndReload and reports whether it succeeded.
func generate(gen *nginxpkg.Generator) bool {
	res, err := gen.GenerateAndReload()
	if err != nil {
		fmt.Println(red("Error: " + err.Error()))
		return false
	}
	if ok, _ := res["ok"].(bool); !ok {
		// Distinguish the two very different failure modes. Printing
		// "nginx test failed" while `nginx -t` actually SUCCEEDED (the real
		// failure being the reload/start) sent troubleshooting down the
		// wrong path entirely.
		t, hasTest := res["test"].(nginxpkg.TestResult)
		if hasTest && !t.OK {
			fmt.Println(red("nginx config test FAILED — the previous config was restored:"))
			fmt.Println(strings.TrimSpace(t.Stderr))
			return false
		}
		fmt.Println(red("The config is valid, but nginx could not be reloaded/started:"))
		if rel, ok := res["reload"].(nginxpkg.TestResult); ok && strings.TrimSpace(rel.Stderr) != "" {
			fmt.Println(strings.TrimSpace(rel.Stderr))
		}
		// `nginx -t` never binds a socket, so a valid config that will not
		// start is almost always a port already owned by another daemon.
		if cs := nginxpkg.FindPortConflicts(nginxpkg.GeneratedFiles(
			nginxpkg.GatewayPath(), nginxpkg.StreamPath())...); len(cs) > 0 {
			fmt.Println(red("Port conflicts (this is the cause):"))
			for _, l := range nginxpkg.DescribeConflicts(cs) {
				fmt.Println("  " + l)
			}
			fmt.Println("  Free the port (stop the other process) or change it in the panel, then generate again.")
		}
		return false
	}
	// A successful generation may still carry warnings worth acting on.
	if t, ok := res["test"].(nginxpkg.TestResult); ok && strings.Contains(t.Stderr, "conflicting server name") {
		fmt.Println(red("Warning: nginx reports conflicting server names — it IGNORES the duplicate"))
		fmt.Println(red("blocks, so the services inside them serve the fake page instead."))
		for _, l := range strings.Split(t.Stderr, "\n") {
			if strings.Contains(l, "conflicting server name") {
				fmt.Println("  " + strings.TrimSpace(l))
			}
		}
		fmt.Println("  These blocks are NOT in Shahrag's generated files — look for leftover")
		fmt.Println("  configs: grep -rn 'server_name' /etc/nginx/conf.d /etc/nginx/sites-enabled")
	}
	fmt.Println(green("Generated and reloaded."))
	return true
}

// RunGenerate is `shahrag generate`. Exits non-zero on failure so the
// installer can detect a failed generation instead of silently proceeding.
func RunGenerate() int {
	cfg := config.New()
	if !generate(nginxpkg.NewGenerator(cfg)) {
		return 1
	}
	return 0
}

// RunStatus is `shahrag status`.
func RunStatus() int {
	cfg := config.New()
	c, _ := cfg.Read()
	fmt.Printf("Shahrag v%s\n", version)
	fmt.Printf("  nginx:     %s\n", yn(nginxpkg.IsActive()))
	fmt.Printf("  web panel: %s\n", yn(systemctlIsActive("shahrag")))
	fmt.Printf("  installed: %v\n", c.Shahrag.Panel.Installed)
	if c.Shahrag.Panel.Installed {
		p := c.Shahrag.Panel
		if p.Domain != "" {
			fmt.Printf("  panel:     https://%s.%s/%s/\n", p.Subdomain, p.Domain, p.Path)
		} else {
			fmt.Printf("  panel:     http://SERVER_IP:%d/%s/\n", p.LocalPort, p.Path)
		}
	}
	return 0
}

func printStatus(cfg *config.Manager, gen *nginxpkg.Generator) {
	c, _ := cfg.Read()
	fmt.Println("\n── Status ──")
	fmt.Printf("  nginx:    %v\n", nginxpkg.IsActive())
	fmt.Printf("  web:      %v\n", systemctlIsActive("shahrag"))
	fmt.Printf("  services: %d\n  domains:  %d\n  reality:  %v\n", len(c.Services), len(c.Domains), c.Reality.Enabled)
	t := gen.Test()
	if t.OK {
		fmt.Println("  config:", green("valid"))
	} else {
		fmt.Println("  config:", red("invalid"))
		fmt.Println(t.Stderr)
	}
}

func systemctlIsActive(name string) bool {
	out, err := exec.Command("systemctl", "is-active", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// ── Helpers ──────────────────────────────────────────────

func mustRead(in *bufio.Reader) string {
	if in == nil {
		return ""
	}
	s, _ := in.ReadString('\n')
	return s
}

func readInt(in *bufio.Reader, prompt string, def int) int {
	fmt.Print(prompt)
	s := strings.TrimSpace(mustRead(in))
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func askYes(in *bufio.Reader, prompt string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	fmt.Printf("%s [%s]: ", prompt, d)
	s := strings.ToLower(strings.TrimSpace(mustRead(in)))
	if s == "" {
		return def
	}
	return s == "y" || s == "yes"
}

func pause(in *bufio.Reader) {
	fmt.Print("\nPress Enter...")
	_ = mustRead(in)
}

func pathStr(p string) string {
	if p == "" || p == "/" {
		return "(root)"
	}
	return p
}

func yn(b bool) string {
	if b {
		return green("active")
	}
	return red("inactive")
}

func containsIntC(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func green(s string) string  { return "\033[32m" + s + "\033[0m" }
func red(s string) string    { return "\033[31m" + s + "\033[0m" }
func yellow(s string) string { return "\033[33m" + s + "\033[0m" }
