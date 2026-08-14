// Package nginx produces gateway.conf (HTTP) and stream-gateway.conf (Reality),
// then tests and reloads nginx. This is a Go port of the original bash generator.
package nginx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"shahrag/internal/config"
)

const defaultFakeHTML = `<!DOCTYPE html><html lang="fa" dir="rtl"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"><title>Loading...</title><style>*{margin:0;padding:0;box-sizing:border-box}body{font-family:sans-serif;display:flex;justify-content:center;align-items:center;min-height:100vh;background:linear-gradient(135deg,#667eea,#764ba2);color:#fff}.c{text-align:center;background:rgba(255,255,255,.1);backdrop-filter:blur(10px);padding:40px 60px;border-radius:16px}h1{font-size:2em;margin-bottom:16px}.s{margin:20px auto;width:40px;height:40px;border:4px solid rgba(255,255,255,.3);border-top-color:#fff;border-radius:50%;animation:r 1s linear infinite}@keyframes r{to{transform:rotate(360deg)}}</style></head><body><div class="c"><h1>سامانه پشتیبانی</h1><div class="s"></div><p>سرور در حال راه‌اندازی است...</p></div></body></html>`

type Generator struct {
	cfg *config.Manager
}

func NewGenerator(cfg *config.Manager) *Generator {
	return &Generator{cfg: cfg}
}

type Result struct {
	HTTPPath   string `json:"http_path"`
	StreamPath string `json:"stream_path"`
}

type TestResult struct {
	OK     bool   `json:"ok"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// Generate writes both config files and returns their paths.
func (g *Generator) Generate() (*Result, error) {
	c, err := g.cfg.Read()
	if err != nil {
		return nil, err
	}

	outPath := c.Nginx.OutputPath
	streamOut := c.Nginx.StreamOutputPath
	fakeDir := c.Nginx.FakeDir
	// Sensible defaults if the config was created by an older version
	// that left these paths empty.
	if outPath == "" {
		outPath = "/etc/nginx/conf.d/gateway.conf"
	}
	if streamOut == "" {
		streamOut = "/etc/nginx/stream-gateway.conf"
	}
	if fakeDir == "" {
		fakeDir = "/var/www/mysite"
	}

	// Remove stale configs from a previous failed/partial generate so
	// that nginx never keeps serving a broken gateway.conf.
	_ = os.Remove(outPath)
	_ = os.Remove(streamOut)

	if err := g.writeFakeSite(c, fakeDir); err != nil {
		return nil, fmt.Errorf("fake site: %w", err)
	}
	if err := g.generateStream(c, streamOut); err != nil {
		return nil, fmt.Errorf("stream: %w", err)
	}
	if err := g.generateHTTP(c, outPath); err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	return &Result{HTTPPath: outPath, StreamPath: streamOut}, nil
}

func (g *Generator) writeFakeSite(c *config.Config, fakeDir string) error {
	if err := os.MkdirAll(fakeDir, 0o755); err != nil {
		return err
	}
	index := filepath.Join(fakeDir, "index.html")
	switch c.FakeSite.Mode {
	case "custom_content":
		return os.WriteFile(index, []byte(c.FakeSite.Content), 0o644)
	case "custom_file":
		src := c.FakeSite.SourcePath
		if src != "" {
			if _, err := os.Stat(src); err == nil {
				return copyFile(src, index)
			}
		}
		return os.WriteFile(index, []byte(defaultFakeHTML), 0o644)
	default:
		return os.WriteFile(index, []byte(defaultFakeHTML), 0o644)
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// ── Stream / Reality ────────────────────────────────────────

func (g *Generator) generateStream(c *config.Config, streamOut string) error {
	if !c.Reality.Enabled {
		_ = os.Remove(streamOut)
		return nil
	}
	var b strings.Builder
	b.WriteString("# =============================================================\n")
	b.WriteString("# nginx-panel Reality Stream Config (Shahrag)\n")
	b.WriteString(fmt.Sprintf("# Generated: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString("# =============================================================\n\n")

	b.WriteString("log_format stream '$remote_addr [$time_local] '\n")
	b.WriteString("                  '$protocol $status $bytes_sent $bytes_received'\n")
	b.WriteString("                  '$session_time';\n\n")
	b.WriteString("access_log /var/log/nginx/stream.log stream;\n\n")

	b.WriteString("map $ssl_preread_server_name $reality_backend {\n")
	// Sort for deterministic output
	names := make([]string, 0, len(c.Reality.Services))
	for n := range c.Reality.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := c.Reality.Services[name]
		fmt.Fprintf(&b, "    # %s\n", name)
		fmt.Fprintf(&b, "    %s    127.0.0.1:%d;\n", svc.SNI, svc.LocalPort)
	}
	fmt.Fprintf(&b, "    default          127.0.0.1:%d;\n", c.Reality.HTTPPort)
	b.WriteString("}\n\n")

	allPorts := map[int]bool{}
	for _, svc := range c.Reality.Services {
		for _, p := range svc.Ports {
			allPorts[p] = true
		}
	}
	ports := make([]int, 0, len(allPorts))
	for p := range allPorts {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	for _, p := range ports {
		fmt.Fprintf(&b, "server {\n    listen %d;\n    listen [::]:%d;\n    proxy_pass $reality_backend;\n    ssl_preread on;\n}\n\n", p, p)
	}

	if err := os.MkdirAll(filepath.Dir(streamOut), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(streamOut, []byte(b.String()), 0o644); err != nil {
		return err
	}

	// Auto-configure nginx.conf to include the stream block
	return g.ensureStreamInclude(streamOut)
}

func (g *Generator) ensureStreamInclude(streamOut string) error {
	const confPath = "/etc/nginx/nginx.conf"
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil // best-effort
	}
	txt := string(data)
	if strings.Contains(txt, streamOut) {
		return nil
	}
	streamRe := regexp.MustCompile(`(?m)^([ \t]*stream[ \t]*\{)`)
	if streamRe.MatchString(txt) {
		txt = streamRe.ReplaceAllString(
			txt, "${1}\n    include "+streamOut+";")
	} else {
		txt += "\n# nginx-panel: Reality stream block\nstream {\n    include " + streamOut + ";\n}\n"
	}
	return os.WriteFile(confPath, []byte(txt), 0o644)
}

// ── HTTP gateway ────────────────────────────────────────────

func (g *Generator) generateHTTP(c *config.Config, outPath string) error {
	var b strings.Builder
	b.WriteString("# =============================================================\n")
	b.WriteString("# nginx-panel HTTP Config (Shahrag)\n")
	b.WriteString(fmt.Sprintf("# Generated: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString("# =============================================================\n\n")

	b.WriteString("map $http_upgrade $connection_upgrade {\n")
	b.WriteString("    default upgrade;\n")
	b.WriteString("    ''      close;\n")
	b.WriteString("}\n\n")

	// HTTP → HTTPS redirect
	if containsInt(c.ListenPorts, 80) {
		b.WriteString("# HTTP → HTTPS redirect\n")
		b.WriteString("server {\n    listen 80 default_server;\n    listen [::]:80 default_server;\n")
		b.WriteString("    server_name _;\n    return 301 https://$host$request_uri;\n}\n\n")
	}

	domains := make([]string, 0, len(c.Domains))
	for d := range c.Domains {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	firstDomain := ""
	if len(domains) > 0 {
		firstDomain = domains[0]
	}

	processedPorts := map[int]bool{}
	for _, port := range c.SortedPorts() {
		if port == 80 {
			continue
		}
		actual := c.EffectivePort(port)
		if processedPorts[actual] {
			continue
		}
		processedPorts[actual] = true

		for _, domain := range domains {
			services := g.servicesForDomainPort(c, domain, port)
			if len(services) == 0 {
				continue
			}
			d := c.Domains[domain]
			isFirst := domain == firstDomain
			ds := ""
			if isFirst {
				ds = " default_server"
			}
			sn := g.serverName(c, domain, services, isFirst)

			fmt.Fprintf(&b, "# ── Domain: %s ──\n", domain)
			fmt.Fprintf(&b, "server {\n")
			fmt.Fprintf(&b, "    listen %d ssl http2%s;\n", actual, ds)
			fmt.Fprintf(&b, "    listen [::]:%d ssl http2%s;\n", actual, ds)
			fmt.Fprintf(&b, "    server_name %s;\n\n", sn)
			fmt.Fprintf(&b, "    ssl_certificate %s;\n", d.Cert)
			fmt.Fprintf(&b, "    ssl_certificate_key %s;\n", d.Key)
			fmt.Fprintf(&b, "    ssl_protocols %s;\n", c.Nginx.SSLProtocols)
			fmt.Fprintf(&b, "    ssl_ciphers %s;\n", c.Nginx.SSLCiphers)
			b.WriteString("    ssl_prefer_server_ciphers on;\n\n")

			hasRoot := false
			for _, svcName := range services {
				svc := c.Services[svcName]
				if svc.Path == "/" {
					hasRoot = true
				}
				subs := g.subsForDomainService(c, svcName, domain)
				b.WriteString(g.locationBlock(svcName, svc, actual, subs, domain))
				b.WriteString("\n")
			}
			if !hasRoot {
				b.WriteString("    # Fake site\n    location / {\n")
				fmt.Fprintf(&b, "        root %s;\n", c.Nginx.FakeDir)
				b.WriteString("        index index.html;\n        try_files $uri $uri/ /index.html;\n    }\n")
			}
			b.WriteString("}\n\n")
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outPath, []byte(b.String()), 0o644)
}

func (g *Generator) servicesForDomainPort(c *config.Config, domain string, port int) []string {
	var out []string
	for name, svc := range c.Services {
		if svc.ListenPort != port {
			continue
		}
		for _, b := range svc.Bindings {
			if b.Domain == domain {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (g *Generator) subsForDomainService(c *config.Config, service, domain string) []string {
	var out []string
	for _, b := range c.Services[service].Bindings {
		if b.Domain == domain {
			out = append(out, b.Subdomain)
		}
	}
	return out
}

func (g *Generator) serverName(c *config.Config, domain string, services []string, isFirst bool) string {
	if isFirst {
		return domain + " *." + domain
	}
	names := map[string]bool{domain: true}
	for _, sv := range services {
		for _, su := range g.subsForDomainService(c, sv, domain) {
			if su != "" {
				names[su+"."+domain] = true
			}
		}
	}
	arr := make([]string, 0, len(names))
	for n := range names {
		arr = append(arr, n)
	}
	sort.Strings(arr)
	return strings.Join(arr, " ")
}

func (g *Generator) locationBlock(name string, svc config.Service, actualPort int, subs []string, domain string) string {
	lp := svc.LocalPort
	sp := svc.Path
	if sp == "" {
		sp = "/"
	}
	po := svc.PathOwned
	sb := svc.SSLBackend

	nonEmpty := []string{}
	for _, s := range subs {
		if s != "" {
			nonEmpty = append(nonEmpty, s)
		}
	}

	var hc string
	switch {
	case len(nonEmpty) == 0:
		hc = "# root domain"
	case len(nonEmpty) == 1:
		hc = fmt.Sprintf(`if ($host != "%s.%s") { return 302 /; }`, nonEmpty[0], domain)
	default:
		alts := make([]string, len(nonEmpty))
		for i, s := range nonEmpty {
			alts[i] = regexp.QuoteMeta(s + "." + domain)
		}
		hc = fmt.Sprintf(`if ($host !~ "^(%s)$") { return 302 /; }`, strings.Join(alts, "|"))
	}

	proto := "http"
	if sb {
		proto = "https"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "    # %s → %s://127.0.0.1:%d\n", name, proto, lp)

	if sp == "/" {
		// Root service owns everything.
		b.WriteString("    location / {\n")
		b.WriteString("        " + hc + "\n")
		fmt.Fprintf(&b, "        proxy_pass %s://127.0.0.1:%d;\n", proto, lp)
		g.writeProxyCommon(&b, sb, actualPort)
		b.WriteString("    }")
		return b.String()
	}

	// Path-based service (the panel itself uses this).
	// The backend serves routes at /, so we strip the prefix AND make
	// sure /<path>/static/* works through the domain.
	fmt.Fprintf(&b, "    location ^~ /%s/static/ {\n", sp)
	b.WriteString("        " + hc + "\n")
	fmt.Fprintf(&b, "        proxy_pass %s://127.0.0.1:%d/static/;\n", proto, lp)
	g.writeProxyCommon(&b, sb, actualPort)
	b.WriteString("    }\n\n")

	fmt.Fprintf(&b, "    location ^~ /%s/api/ {\n", sp)
	b.WriteString("        " + hc + "\n")
	fmt.Fprintf(&b, "        proxy_pass %s://127.0.0.1:%d/api/;\n", proto, lp)
	g.writeProxyCommon(&b, sb, actualPort)
	b.WriteString("    }\n\n")

	fmt.Fprintf(&b, "    location = /%s {\n", sp)
	fmt.Fprintf(&b, "        return 302 /%s/;\n", sp)
	b.WriteString("    }\n\n")

	fmt.Fprintf(&b, "    location ^~ /%s/ {\n", sp)
	b.WriteString("        " + hc + "\n")
	// Rewrite /<path>/foo -> /foo on the backend.
	fmt.Fprintf(&b, "        rewrite ^/%s/(.*)$ /$1 break;\n", sp)
	fmt.Fprintf(&b, "        proxy_pass %s://127.0.0.1:%d;\n", proto, lp)
	g.writeProxyCommon(&b, sb, actualPort)
	if !po {
		fmt.Fprintf(&b, "        proxy_redirect %s://127.0.0.1:%d/ /%s/;\n", proto, lp, sp)
		fmt.Fprintf(&b, "        proxy_redirect / /%s/;\n", sp)
	}
	b.WriteString("    }")
	return b.String()
}

// writeProxyCommon writes the shared proxy_set_header block used by every
// location that forwards traffic to a backend.
func (g *Generator) writeProxyCommon(b *strings.Builder, sslBackend bool, actualPort int) {
	b.WriteString("        proxy_http_version 1.1;\n")
	b.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
	b.WriteString("        proxy_set_header Connection $connection_upgrade;\n")
	b.WriteString("        proxy_set_header Host $host;\n")
	b.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
	b.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	b.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	if sslBackend {
		b.WriteString("        proxy_set_header X-Forwarded-Host $host;\n")
		fmt.Fprintf(b, "        proxy_set_header X-Forwarded-Port %d;\n", actualPort)
		b.WriteString("        proxy_ssl_verify off;\n        proxy_ssl_server_name off;\n        proxy_buffering off;\n")
	}
	b.WriteString("        proxy_read_timeout 86400s;\n")
	b.WriteString("        proxy_send_timeout 86400s;\n")
}

// ── nginx operations ────────────────────────────────────────

func (g *Generator) Test() TestResult {
	cmd := exec.Command("nginx", "-t")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return TestResult{
		OK:     err == nil,
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
}

func (g *Generator) Reload() TestResult {
	cmd := exec.Command("systemctl", "reload", "nginx")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return TestResult{
		OK:     err == nil,
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
}

func (g *Generator) GenerateAndReload() (map[string]interface{}, error) {
	res := map[string]interface{}{}
	paths, err := g.Generate()
	if err != nil {
		return res, err
	}
	res["paths"] = paths
	test := g.Test()
	res["test"] = test
	if !test.OK {
		res["ok"] = false
		return res, nil
	}
	rel := g.Reload()
	res["reload"] = rel
	res["ok"] = rel.OK
	return res, nil
}

// ── Helpers ─────────────────────────────────────────────────

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
