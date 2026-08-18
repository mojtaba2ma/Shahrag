// Package cli — `shahrag doctor`: a one-shot diagnostic report that helps
// troubleshoot a broken panel without touching anything.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
	"shahrag/internal/systemd"
)

// RunDoctor prints a human-readable health report and exits.
func RunDoctor() int {
	fmt.Printf("Shahrag v%s — doctor report\n", version)
	fmt.Println("════════════════════════════════════════════")

	// Config
	cfg := config.New()
	c, err := cfg.Read()
	if err != nil {
		fmt.Printf("config: %sREAD ERROR%s %v\n", "\033[31m", "\033[0m", err)
		fmt.Printf("config path: %s\n", config.ConfigPath)
	} else {
		fmt.Printf("config: OK (%s)\n", config.ConfigPath)
		fmt.Printf("  installed: %v\n", c.Shahrag.Panel.Installed)
		fmt.Printf("  panel: domain=%q sub=%q local_port=%d listen_port=%d path=%q\n",
			c.Shahrag.Panel.Domain, c.Shahrag.Panel.Subdomain,
			c.Shahrag.Panel.LocalPort, c.Shahrag.Panel.ListenPort, c.Shahrag.Panel.Path)
		if c.Shahrag.Panel.Domain != "" {
			fmt.Printf("  panel URL: https://%s.%s/%s/\n", c.Shahrag.Panel.Subdomain, c.Shahrag.Panel.Domain, c.Shahrag.Panel.Path)
		} else {
			fmt.Printf("  panel URL: http://SERVER_IP:%d/%s/\n", c.Shahrag.Panel.LocalPort, c.Shahrag.Panel.Path)
		}
		fmt.Printf("  domains: %d   services: %d   reality: %v (services: %d)\n",
			len(c.Domains), len(c.Services), c.Reality.Enabled, len(c.Reality.Services))
		fmt.Printf("  listen_ports: %v\n", c.SortedPorts())
		for _, p := range c.ListenPorts {
			if p == 80 {
				continue
			}
			if eff := c.EffectivePort(p); eff != p {
				fmt.Printf("  port %d is owned by Reality → HTTP services on it are served on the Reality HTTP port %d\n", p, eff)
			}
		}

		// Services without bindings are the classic cause of "fake page
		// instead of service".
		var noBind []string
		for name, svc := range c.Services {
			if len(svc.Bindings) == 0 {
				noBind = append(noBind, name)
			}
		}
		sort.Strings(noBind)
		if len(noBind) > 0 {
			fmt.Printf("  %sWARNING%s services without a domain binding (they will not be generated): %s\n",
				"\033[33m", "\033[0m", strings.Join(noBind, ", "))
		}
		for name, d := range c.Domains {
			if d.Cert == "" || d.Key == "" {
				fmt.Printf("  %sWARNING%s domain %q has no certificate/key — its server block is skipped\n",
					"\033[33m", "\033[0m", name)
			}
		}
	}

	// nginx
	fmt.Println("──── nginx ───────────────────────────────────")
	fmt.Printf("  service: %s\n", yn(systemd.IsActive("nginx")))
	t := nginxpkg.NewGenerator(cfg).Test()
	if t.OK {
		fmt.Printf("  nginx -t: %s\n", green("valid"))
	} else {
		fmt.Printf("  nginx -t: %s\n%s\n", red("INVALID"), strings.TrimSpace(t.Stderr))
	}
	fmt.Printf("  version: %s\n", strings.TrimSpace(nginxpkg.Version()))
	fmt.Printf("  worker_connections: %d   cache_enabled: %v   log_level: %s\n",
		nginxpkg.WorkerConnections(), nginxpkg.CacheEnabled(), nginxpkg.LogLevel())

	// ── Boot readiness ─────────────────────────────────────────────
	// "nginx is inactive after a reboot but nginx -t is valid" has three
	// classic causes; report each of them explicitly.
	fmt.Println("──── nginx boot readiness ────────────────────")
	if nginxpkg.IsEnabled() {
		fmt.Printf("  enabled at boot: %s\n", green("yes"))
	} else {
		fmt.Printf("  enabled at boot: %s  → run: sudo shahrag boot-guard\n", red("NO — nginx will NOT start after a reboot"))
	}
	if nginxpkg.DropInInstalled() {
		fmt.Printf("  systemd drop-in: %s\n", green(nginxpkg.SystemdDropInSummary()))
	} else {
		fmt.Printf("  systemd drop-in: %s%s%s  (no automatic retry if the first boot attempt fails)\n",
			"\033[33m", nginxpkg.SystemdDropInSummary(), "\033[0m")
	}
	wc := nginxpkg.WorkerConnections()
	rl := nginxpkg.WorkerRLimit()
	if wc > 1024 && rl < wc {
		fmt.Printf("  %sWARNING%s worker_connections=%d but worker_rlimit_nofile=%d — nginx logs\n",
			"\033[33m", "\033[0m", wc, rl)
		fmt.Printf("          \"worker_connections exceed open file resource limit\". Fix: sudo shahrag boot-guard\n")
	} else if rl > 0 {
		fmt.Printf("  worker_rlimit_nofile: %d\n", rl)
	}
	if !systemd.IsActive("nginx") {
		if reason := nginxpkg.LastFailureReason(); reason != "" {
			fmt.Printf("  %swhy nginx is not running%s:\n", "\033[31m", "\033[0m")
			for _, l := range strings.Split(reason, "\n") {
				fmt.Printf("    %s\n", l)
			}
		}
	}
	fmt.Println("──── nginx files ─────────────────────────────")
	for _, f := range []string{"/etc/nginx/conf.d/gateway.conf", "/etc/nginx/stream-gateway.conf"} {
		if _, err := os.Stat(f); err == nil {
			fmt.Printf("  %s: exists\n", f)
		} else {
			fmt.Printf("  %s: %smissing%s\n", f, "\033[33m", "\033[0m")
		}
	}

	// Panel service
	fmt.Println("──── shahrag service ─────────────────────────")
	fmt.Printf("  service: %s\n", yn(systemd.IsActive("shahrag")))
	out, err := exec.Command("ss", "-ltnp").CombinedOutput()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		var panelLines []string
		for _, l := range lines {
			if strings.Contains(l, "shahrag") {
				panelLines = append(panelLines, strings.TrimSpace(l))
			}
		}
		if len(panelLines) == 0 {
			fmt.Printf("  %sno listen socket owned by shahrag%s\n", "\033[31m", "\033[0m")
		}
		for _, l := range panelLines {
			fmt.Printf("  %s\n", l)
		}
	} else {
		fmt.Println("  ss not available")
	}

	// Backups
	fmt.Println("──── backups ─────────────────────────────────")
	if entries, err := os.ReadDir("/var/backups/shahrag"); err == nil {
		if len(entries) == 0 {
			fmt.Println("  none yet")
		}
		for _, e := range entries {
			fmt.Printf("  /var/backups/shahrag/%s\n", e.Name())
		}
	} else {
		fmt.Println("  /var/backups/shahrag not present")
	}

	// Dump the config as JSON at the end for easy copying.
	if c != nil {
		fmt.Println("──── config.json ─────────────────────────────")
		data, _ := json.MarshalIndent(c, "", "  ")
		fmt.Println(string(data))
	}
	fmt.Println()
	fmt.Println("For live routing tests of every service, run: sudo shahrag selftest")
	return 0
}
