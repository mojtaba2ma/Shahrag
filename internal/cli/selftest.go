// Package cli — `shahrag selftest`: on-server end-to-end checks for every
// service. It exercises the SAME paths real traffic takes: the backend
// socket, the route through the Reality HTTP port, and the route through
// the Reality listen port (the stream default — what Cloudflare actually
// hits).
//
// Classification is realistic:
//   - backends that ACCEPT TCP but do not answer plain HTTP (xray/x-ui
//     proxy inbounds) are reported as TCP-OPEN warnings, not failures.
//   - routing: an HTTP code (even 4xx) means nginx reached the backend —
//     that is a PASS for routing. Only connection failures/timeouts are
//     flagged red.
//   - for failing routes the service's generated location block is printed
//     so the operator can see exactly what nginx is doing.
package cli

import (
	"fmt"
	"net"
	"os"
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

// tcpProbe dials 127.0.0.1:port (2s timeout) and reports whether anything
// listens there.
func tcpProbe(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// httpProbe returns (statusCode, curlExitCode). statusCode is "" when curl
// could not obtain a response (000).
func httpProbe(url string, timeoutSec int) (string, int) {
	args := []string{"-sk", "-m", strconv.Itoa(timeoutSec), "--connect-timeout", "2",
		"-o", "/dev/null", "-w", "%{http_code}", url}
	cmd := exec.Command("curl", args...)
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	code := strings.TrimSpace(string(out))
	if code == "000" {
		code = ""
	}
	return code, exit
}

// backendStatus classifies a backend:
//
//	"OK (http 200)"         — answers HTTP(S)
//	"TCP-OPEN"              — accepts TCP but no HTTP reply (proxy inbound)
//	"DOWN"                  — nothing listens
type backendStatus struct {
	Kind string // OK | TCP-OPEN | DOWN
	Code string
}

func probeBackend(port int, ssl bool) backendStatus {
	if !tcpProbe(port) {
		return backendStatus{Kind: "DOWN"}
	}
	scheme := "http"
	if ssl {
		scheme = "https"
	}
	code, _ := httpProbe(fmt.Sprintf("%s://127.0.0.1:%d/", scheme, port), 3)
	if code != "" {
		return backendStatus{Kind: "OK", Code: code}
	}
	return backendStatus{Kind: "TCP-OPEN"}
}

// routeResult captures one routing test.
type routeResult struct {
	Code string // HTTP code ("200") or "" on failure
	Exit int    // curl exit code (7 refused, 28 timeout, ...)
}

// routeTest requests https://host:port/path via 127.0.0.1 (--resolve keeps
// the SNI/Host correct). Any HTTP code means nginx answered.
func routeTest(host string, port int, path string) routeResult {
	if _, err := exec.LookPath("curl"); err != nil {
		return routeResult{Exit: -1}
	}
	url := fmt.Sprintf("https://%s:%d%s", host, port, path)
	args := []string{"-sk", "-m", "8", "--connect-timeout", "3",
		"-o", "/dev/null", "-w", "%{http_code}",
		"--resolve", fmt.Sprintf("%s:%d:127.0.0.1", host, port),
		url}
	cmd := exec.Command("curl", args...)
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	code := strings.TrimSpace(string(out))
	if code == "000" {
		code = ""
	}
	return routeResult{Code: code, Exit: exit}
}

func routeLabel(r routeResult) string {
	if r.Code != "" {
		n := atoiSafe(r.Code)
		switch {
		case n >= 200 && n < 400:
			return green("OK") + " (" + r.Code + ")"
		default:
			// nginx answered; the 4xx/5xx came from the backend itself.
			return yellow("BACKEND-ANSWER") + " (" + r.Code + ")"
		}
	}
	switch r.Exit {
	case 28:
		return red("TIMEOUT (backend accepted but never answered)")
	case 7:
		return red("CONN-REFUSED")
	default:
		return red("NO-RESPONSE (exit " + strconv.Itoa(r.Exit) + ")")
	}
}

// printLocationBlock prints the generated nginx block for a service from
// gateway.conf (helper for debugging failing routes).
func printLocationBlock(service string) {
	outPath := "/etc/nginx/conf.d/gateway.conf"
	data, err := os.ReadFile(outPath)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	marker := "    # " + service + " →"
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, marker) {
			start = i
			break
		}
	}
	if start < 0 {
		fmt.Printf("  (no generated block found in %s for %q)\n", outPath, service)
		return
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "    }") {
			end = i + 1
			break
		}
	}
	fmt.Println("  generated block:")
	for _, l := range lines[start:end] {
		fmt.Println("    " + l)
	}
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

	// ── 1. Listeners ──
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

	// ── 2. Backends + 3. Routing, per service ──
	fmt.Println("── services (backend + routing) ──")
	for _, name := range sortedServiceNames(c) {
		svc := c.Services[name]
		bs := probeBackend(svc.LocalPort, svc.SSLBackend)

		// Routing tests first (they determine whether the service works).
		type rt struct {
			host string
			port int
			path string
			via  string
			res  routeResult
		}
		var tests []rt
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
			tests = append(tests, rt{host: host, port: eff, path: p, via: "direct", res: routeTest(host, eff, p)})
			if c.Reality.Enabled && eff != svc.ListenPort {
				tests = append(tests, rt{host: host, port: svc.ListenPort, path: p, via: "listen", res: routeTest(host, svc.ListenPort, p)})
			}
		}
		anyRouting := false
		allRoutingFail := true
		for _, tt := range tests {
			if tt.res.Code != "" {
				anyRouting = true
				allRoutingFail = false
			}
		}

		fmt.Printf("  %-16s local:%d  backend: %s\n", name, svc.LocalPort, backendLabel(bs))
		for _, tt := range tests {
			fmt.Printf("      %-32s :%-5d %-18s %s  → %s\n", tt.host, tt.port, tt.path, tt.via, routeLabel(tt.res))
		}

		// Failure accounting: a service fails only when routing could not
		// reach nginx/backend at all. A 4xx/5xx HTTP answer counts as
		// "reached" (the backend answered). Proxy-type backends (TCP-OPEN)
		// never answer plain HTTP probes, so a timeout there is EXPECTED
		// behaviour — real clients speak the service's own protocol.
		if allRoutingFail && len(tests) > 0 {
			if bs.Kind == "TCP-OPEN" {
				fmt.Printf("  %s proxy-type backend: plain-HTTP probes cannot verify it (expected for xray/x-ui inbounds).\n", yellow("⚠"))
				fmt.Println("      Test it with a real client — if it answers there, it is working.")
			} else {
				failed++
				fmt.Printf("  %s routing cannot reach this service\n", red("✗"))
				printLocationBlock(name)
			}
		}
		// Nothing listening on the local port is a hard failure regardless
		// of what nginx returns (nginx would answer 502 for it).
		if bs.Kind == "DOWN" {
			failed++
		}
		if !anyRouting && len(tests) == 0 {
			fmt.Printf("  %s service has no domain bindings\n", yellow("⚠"))
		}
	}

	// ── 4. Active nginx config vs disk ──
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
			fmt.Println("  " + red("WARNING: nginx is still running an OLD config — reload did not take effect."))
			fmt.Println("  " + red("Run: systemctl reload nginx"))
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
		fmt.Printf("%s — %d service(s) unreachable through nginx\n", red("FAIL"), failed)
		return 1
	}
	fmt.Println(green("ALL CHECKS PASSED"))
	return 0
}

func backendLabel(bs backendStatus) string {
	switch bs.Kind {
	case "OK":
		return green("OK") + " (http " + bs.Code + ")"
	case "TCP-OPEN":
		return yellow("TCP-OPEN") + " (accepts connections but no plain-HTTP reply — proxy-type backend, expected for xray/x-ui inbounds)"
	case "DOWN":
		return red("DOWN") + " (nothing listening on this port)"
	}
	return bs.Kind
}
