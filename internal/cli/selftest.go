// Package cli — `shahrag selftest`: on-server end-to-end checks for every
// service. It exercises the SAME paths real traffic takes: the backend
// socket, the route through the Reality HTTP port, and the route through
// the Reality listen port (the stream default — what Cloudflare actually
// hits). Prints a PASS/FAIL report; exit code 0 = all checks passed.
package cli

import (
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/systemd"
)

func sortedServiceNames(c *config.Config) []string {
	names := make([]string, 0, len(c.Services))
	for n := range c.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// probeBackend checks whether the backend answers on 127.0.0.1:port and
// returns the HTTP status code (or "tcp-open" when curl is unavailable).
// Returns "" when nothing answers.
func probeBackend(port int, ssl bool) string {
	if _, err := exec.LookPath("curl"); err != nil {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
		if err != nil {
			return ""
		}
		conn.Close()
		return "tcp-open"
	}
	scheme := "http"
	args := []string{"-s", "-m", "3", "--connect-timeout", "2", "-o", "/dev/null", "-w", "%{http_code}"}
	if ssl {
		args = append(args, "-k")
	}
	args = append(args, fmt.Sprintf("%s://127.0.0.1:%d/", scheme, port))
	out, err := exec.Command("curl", args...).CombinedOutput()
	if err != nil {
		return ""
	}
	code := strings.TrimSpace(string(out))
	if code == "000" {
		return ""
	}
	return code
}

// routeTest requests https://host:port/path on 127.0.0.1 (via --resolve so
// the SNI/Host stay correct) and returns the HTTP status code, or "" when
// the connection fails. Any HTTP code means nginx answered; a non-200 code
// usually comes from the backend itself.
func routeTest(host string, port int, path string) string {
	if _, err := exec.LookPath("curl"); err != nil {
		return "curl-not-found"
	}
	url := fmt.Sprintf("https://%s:%d%s", host, port, path)
	args := []string{"-sk", "-m", "6", "--connect-timeout", "3",
		"-o", "/dev/null", "-w", "%{http_code}",
		"--resolve", fmt.Sprintf("%s:%d:127.0.0.1", host, port),
		url}
	out, err := exec.Command("curl", args...).CombinedOutput()
	if err != nil {
		return ""
	}
	code := strings.TrimSpace(string(out))
	if code == "000" {
		return ""
	}
	return code
}

// RunSelfTest is the `shahrag selftest` entry point.
func RunSelfTest() int {
	cfg := config.New()
	c, err := cfg.Read()
	if err != nil {
		fmt.Printf("cannot read config: %v\n", err)
		return 1
	}
	fmt.Printf("Shahrag v%s — self-test\n", version)
	fmt.Println("════════════════════════════════════════════")
	failed := 0

	// ── 1. Listeners: who owns the configured ports? ──
	fmt.Println("── listeners (which process owns each port?) ──")
	out, _ := exec.Command("ss", "-ltnp").CombinedOutput()
	lines := strings.Split(string(out), "\n")
	want := map[string]bool{}
	for _, p := range c.ListenPorts {
		want[fmt.Sprintf(":%d ", p)] = true
	}
	want[fmt.Sprintf(":%d ", c.Reality.HTTPPort)] = true
	for _, svc := range c.Reality.Services {
		for _, p := range svc.Ports {
			want[fmt.Sprintf(":%d ", p)] = true
		}
	}
	for _, svc := range c.Services {
		want[fmt.Sprintf(":%d ", svc.LocalPort)] = true
	}
	printed := false
	for _, l := range lines {
		for p := range want {
			if strings.Contains(l, p) {
				fmt.Println("  " + strings.TrimSpace(l))
				printed = true
				break
			}
		}
	}
	if !printed {
		fmt.Println("  (no listeners on the configured ports — nginx or backends are down)")
	}

	// ── 2. Backends ──
	fmt.Println("── backends (127.0.0.1:local_port) ──")
	for _, name := range sortedServiceNames(c) {
		svc := c.Services[name]
		code := probeBackend(svc.LocalPort, svc.SSLBackend)
		if code == "" {
			fmt.Printf("  %-16s :%-6d %s\n", name, svc.LocalPort, red("DOWN"))
			failed++
		} else {
			fmt.Printf("  %-16s :%-6d %s (http %s)\n", name, svc.LocalPort, green("OK"), code)
		}
	}

	// ── 3. Routing: direct to the effective port AND via the listen port ──
	fmt.Println("── routing ──")
	for _, name := range sortedServiceNames(c) {
		svc := c.Services[name]
		eff := c.EffectivePort(svc.ListenPort)
		for _, b := range svc.Bindings {
			host := b.Domain
			if b.Subdomain != "" {
				host = b.Subdomain + "." + b.Domain
			}
			p := svc.Path
			if p == "" || p == "/" {
				p = "/"
			} else {
				p = "/" + p + "/"
			}
			direct := routeTest(host, eff, p)
			dm := green("OK") + " (" + direct + ")"
			if direct == "" {
				dm = red("NO-RESPONSE")
				failed++
			}
			fmt.Printf("  %-16s %-28s :%d%-24s direct   → %s\n", name, host, eff, p, dm)

			// The REAL Cloudflare path: connect to the reality listen port;
			// the stream block (ssl_preread) must route unknown SNIs to the
			// HTTP port via its default backend.
			if c.Reality.Enabled && eff != svc.ListenPort {
				via := routeTest(host, svc.ListenPort, p)
				vm := green("OK") + " (" + via + ")"
				if via == "" {
					vm = red("NO-RESPONSE")
					failed++
				}
				fmt.Printf("  %-16s %-28s :%d%-24s via listen port → %s\n", "", host, svc.ListenPort, p, vm)
			}
		}
	}

	// ── 4. Active nginx config vs files on disk ──
	fmt.Println("── nginx active config (nginx -T) ──")
	tOut, err := exec.Command("nginx", "-T").CombinedOutput()
	if err != nil {
		fmt.Println("  nginx -T failed: " + err.Error())
		failed++
	} else {
		txt := string(tOut)
		httpPort := c.Reality.HTTPPort
		active := strings.Count(txt, fmt.Sprintf("listen %d ssl", httpPort))
		diskOut, _ := exec.Command("grep", "-c", fmt.Sprintf("listen %d ssl", httpPort), "/etc/nginx/conf.d/gateway.conf").CombinedOutput()
		disk := atoiSafe(string(diskOut))
		fmt.Printf("  server blocks on %d — active: %d, on disk: %d\n", httpPort, active, disk)
		if active != disk {
			fmt.Println("  " + red("WARNING: nginx is still running an OLD config — the reload did not take effect."))
			fmt.Println("  " + red("Check the listeners above for a port conflict (e.g. xray bound 443 directly)"))
			fmt.Println("  " + red("and run: systemctl reload nginx"))
			failed++
		}
		fmt.Printf("  stream (reality) blocks in active config: %d\n", strings.Count(txt, "reality_backend"))
	}

	// ── 5. Panel service ──
	fmt.Println("── panel service ──")
	fmt.Printf("  shahrag: %s\n", yn(systemd.IsActive("shahrag")))
	if !c.Shahrag.Panel.Installed {
		fmt.Println("  panel wizard: not completed yet (installed=false)")
	}

	fmt.Println("════════════════════════════════════════════")
	if failed > 0 {
		fmt.Printf("%s — %d check(s) failed\n", red("FAIL"), failed)
		return 1
	}
	fmt.Println(green("ALL CHECKS PASSED"))
	return 0
}
