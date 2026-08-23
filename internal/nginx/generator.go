// Package nginx produces gateway.conf (HTTP) and stream-gateway.conf (Reality),
// then tests and reloads nginx. This is a Go port of the original bash generator.
//
// Safety guarantees:
//   - Every generated file (and nginx.conf, when it must be edited) is
//     snapshotted first; if `nginx -t` or the reload fails, the previous
//     files are restored and the running nginx keeps its old configuration.
//   - Domains without a certificate are skipped (a comment is emitted) so a
//     half-configured domain can never break `nginx -t`.
//   - The Reality stream include in nginx.conf is clearly marked and is
//     removed again when Reality is disabled, so a dangling include can
//     never block nginx from starting.
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

// Marker comments used to fence the stream block Shahrag manages inside
// nginx.conf. Anything between the markers is ours and safe to remove.
const (
	streamMarkerBegin = "# nginx-panel: Reality stream block (managed by Shahrag)"
	streamMarkerEnd   = "# end nginx-panel stream block"
)

// DefaultNginxConf is the path of the main nginx configuration.
const DefaultNginxConf = "/etc/nginx/nginx.conf"

type Generator struct {
	cfg *config.Manager
	// NginxConf is the nginx.conf path used by this generator. Defaults to
	// /etc/nginx/nginx.conf; tests override it.
	NginxConf string
}

func NewGenerator(cfg *config.Manager) *Generator {
	return &Generator{cfg: cfg}
}

func (g *Generator) confPath() string {
	if g.NginxConf != "" {
		return g.NginxConf
	}
	return DefaultNginxConf
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

// Generate writes both config files and returns their paths. The caller is
// responsible for backup/rollback (see GenerateAndReload); Generate itself
// never deletes a file it does not also rewrite or explicitly clean up.
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

// mapKeyForSNI renders the left-hand side of a stream map entry.
//
// A leading "*." (or a bare "~" regex the operator typed themselves) must be
// emitted as an nginx regex, otherwise "*.example.com" is treated as a
// literal hostname and never matches. Wildcards are what make
// "send every subdomain of this site out to the internet" practical.
func mapKeyForSNI(sni string) string {
	sni = strings.TrimSpace(sni)
	if strings.HasPrefix(sni, "~") {
		return sni // already a regex, pass through untouched
	}
	if strings.HasPrefix(sni, "*.") {
		base := strings.TrimPrefix(sni, "*.")
		// Match the bare domain AND any subdomain, case-insensitively.
		return "~*^(.+\\.)?" + regexp.QuoteMeta(base) + "$"
	}
	return sni
}

// streamUpstream renders the right-hand side of a stream map entry.
//
//   - local target      → 127.0.0.1:<local_port>   (unchanged behaviour)
//   - passthrough       → $ssl_preread_server_name:<port>, i.e. connect to
//     the site the client actually asked for. This is
//     the SNI-proxy trick that lets the server act as
//     an unblocker/exit for chosen domains without
//     terminating TLS.
//   - explicit hostname → host:<local_port>
func streamUpstream(svc config.RealityService) string {
	port := svc.LocalPort
	if port <= 0 {
		port = 443
	}
	if strings.TrimSpace(svc.Target) == config.PassthroughTarget {
		return fmt.Sprintf("$ssl_preread_server_name:%d", port)
	}
	return fmt.Sprintf("%s:%d", config.ResolveTarget(svc.Target), port)
}

// ── Stream / SNI routing ────────────────────────────────────

func (g *Generator) generateStream(c *config.Config, streamOut string) error {
	if !c.Reality.Enabled {
		// Keep a comment-only file so an include left behind by an older
		// version can never make `nginx -t` fail, and remove OUR include
		// from nginx.conf.
		dir := filepath.Dir(streamOut)
		_ = os.MkdirAll(dir, 0o755)
		comment := fmt.Sprintf("# Shahrag: Reality is disabled. Generated %s.\n", time.Now().Format("2006-01-02 15:04:05"))
		if err := os.WriteFile(streamOut, []byte(comment), 0o644); err != nil {
			return err
		}
		return g.removeStreamInclude(streamOut)
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

	// A resolver is MANDATORY as soon as any rule forwards to a hostname
	// taken from a variable (passthrough rules use
	// $ssl_preread_server_name). Without it nginx accepts the config and
	// then fails every such connection at runtime with
	// "no resolver defined to resolve <host>" — verified against a real
	// nginx. It is harmless when no rule needs it, so it is always emitted.
	resolvers := c.Reality.Resolvers
	if len(resolvers) == 0 {
		resolvers = config.DefaultResolvers()
	}
	// A resolver that points at this machine would send pass-through rules
	// straight back here (the local AdGuard is the one rewriting those very
	// domains), so nginx would connect to itself until worker_connections
	// runs out. Fall back to the public defaults rather than emit a config
	// that melts down on the first request.
	if err := config.ValidateResolvers(resolvers); err != nil {
		fmt.Fprintf(&b, "# WARNING: configured resolver rejected — %v\n", err)
		fmt.Fprintf(&b, "# Falling back to the public defaults to prevent a routing loop.\n")
		resolvers = config.DefaultResolvers()
	}
	fmt.Fprintf(&b, "resolver %s valid=300s ipv6=off;\nresolver_timeout 5s;\n\n",
		strings.Join(resolvers, " "))

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
		fmt.Fprintf(&b, "    %s    %s;\n", mapKeyForSNI(svc.SNI), streamUpstream(svc))
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

// ensureStreamInclude adds a marked stream{} block to nginx.conf when it is
// missing. The edit is validated with `nginx -t` and rolled back on failure.
// When the generator uses a test fixture path (NginxConf set), no real nginx
// validation is performed.
func (g *Generator) ensureStreamInclude(streamOut string) error {
	confPath := g.confPath()
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil // best-effort: no nginx.conf on this system
	}
	txt := string(data)
	if strings.Contains(txt, streamOut) {
		return nil
	}
	newTxt := addStreamInclude(txt, streamOut)
	if newTxt == txt {
		return nil
	}
	bak, err := SnapBackup(confPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(confPath, []byte(newTxt), 0o644); err != nil {
		return err
	}
	if g.NginxConf != "" {
		return nil // fixture mode
	}
	if t := g.Test(); !t.OK {
		_ = bak.Restore()
		// nginx built without the stream module cannot serve Reality
		// streams; that is a server capability, not a config error. Skip
		// the include so the rest of the generation still succeeds.
		if strings.Contains(t.Stderr, `unknown directive "stream"`) {
			return nil
		}
		return fmt.Errorf("nginx.conf stream include rejected by nginx -t: %s", strings.TrimSpace(t.Stderr))
	}
	return nil
}

// removeStreamInclude removes the marked stream block (and any unmarked
// include of our file) from nginx.conf. Validated and rolled back on failure.
func (g *Generator) removeStreamInclude(streamOut string) error {
	confPath := g.confPath()
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil // nothing to edit
	}
	txt := string(data)
	newTxt := removeStreamInclude(txt, streamOut)
	if newTxt == txt {
		return nil
	}
	bak, err := SnapBackup(confPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(confPath, []byte(newTxt), 0o644); err != nil {
		return err
	}
	if g.NginxConf != "" {
		return nil // fixture mode
	}
	if t := g.Test(); !t.OK {
		_ = bak.Restore()
		return fmt.Errorf("nginx.conf stream cleanup rejected by nginx -t: %s", strings.TrimSpace(t.Stderr))
	}
	return nil
}

// addStreamInclude is a pure text helper: it appends a marked stream block
// containing `include <streamOut>;` at the end of the configuration text.
func addStreamInclude(txt, streamOut string) string {
	if strings.Contains(txt, streamOut) {
		return txt
	}
	block := fmt.Sprintf("\n%s\nstream {\n    include %s;\n}\n%s\n",
		streamMarkerBegin, streamOut, streamMarkerEnd)
	return strings.TrimRight(txt, "\n") + "\n" + block
}

// removeStreamInclude is a pure text helper: it removes the marked stream
// block and any bare `include <streamOut>;` line that Shahrag may have added.
func removeStreamInclude(txt, streamOut string) string {
	out := txt
	// Remove the fully-marked block.
	re := regexp.MustCompile(`(?ms)\n?` + regexp.QuoteMeta(streamMarkerBegin) + `.*?` + regexp.QuoteMeta(streamMarkerEnd) + `\n?`)
	out = re.ReplaceAllString(out, "\n")
	// Remove a bare include of our stream file on its own line.
	inc := regexp.QuoteMeta(streamOut)
	re2 := regexp.MustCompile(`(?m)^[ \t]*include[ \t]+` + inc + `;[ \t]*\n`)
	out = re2.ReplaceAllString(out, "")
	// Drop a now-empty stream block that only ever contained our include.
	re3 := regexp.MustCompile(`(?m)^[ \t]*stream[ \t]*\{[ \t]*\n[ \t]*\}[ \t]*\n`)
	out = re3.ReplaceAllString(out, "")
	return out
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

	// A resolver is only needed when a service proxies to a HOSTNAME rather
	// than to 127.0.0.1. nginx resolves literal upstream names once at
	// startup, so this is not strictly required for a static hostname — but
	// it keeps long-lived configs working when the remote host's DNS record
	// changes, and costs nothing otherwise. It is emitted only when at
	// least one service actually points off-box, so existing local-only
	// setups produce byte-identical output to before.
	needsResolver := false
	for _, svc := range c.Services {
		if !config.IsLocalTarget(svc.Target) {
			needsResolver = true
			break
		}
	}
	if needsResolver {
		res := c.Reality.Resolvers
		if len(res) == 0 {
			res = config.DefaultResolvers()
		}
		fmt.Fprintf(&b, "resolver %s valid=300s ipv6=off;\nresolver_timeout 5s;\n\n",
			strings.Join(res, " "))
	}

	// Surface services that have no domain binding — they would otherwise
	// silently produce no server block at all.
	svcNames := make([]string, 0, len(c.Services))
	for n := range c.Services {
		svcNames = append(svcNames, n)
	}
	sort.Strings(svcNames)
	for _, n := range svcNames {
		if len(c.Services[n].Bindings) == 0 {
			fmt.Fprintf(&b, "# ── Service %q skipped: no domain binding (panel → Services → bindings) ──\n\n", n)
		}
	}

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

	// Server blocks are grouped by EFFECTIVE listen port and by canonical
	// (case-insensitive) domain.
	//
	// Two real-world bugs made this necessary:
	//   1. When Reality owns several ports (443 and 2053) they all remap to
	//      the same Reality HTTP port (6038). Emitting one block per
	//      ORIGINAL port produced several server blocks on 6038 carrying
	//      the same server_name → nginx printed
	//      `conflicting server name "sugerdood.com" on 0.0.0.0:6038,
	//      ignored` and silently dropped one of them, so the services in
	//      the ignored block became unreachable (fake page instead).
	//   2. Case-variant domain keys ("Sugerdood.com" vs "sugerdood.com")
	//      produced two blocks for what is the SAME host, splitting the
	//      locations of one FQDN across two blocks — only one of them won.
	//
	// Grouping by (effective port, lowercase domain) makes every hostname
	// appear in exactly one server block, which is also what the CLI panel
	// produces on a clean config.
	effPorts := map[int][]int{}
	for _, port := range c.SortedPorts() {
		if port == 80 {
			continue
		}
		eff := c.EffectivePort(port)
		effPorts[eff] = append(effPorts[eff], port)
	}
	effList := make([]int, 0, len(effPorts))
	for p := range effPorts {
		effList = append(effList, p)
	}
	sort.Ints(effList)

	// Canonical domain groups: lowercase name → the config keys that spell
	// it (usually one).
	lowerKeys := map[string][]string{}
	for _, d := range domains {
		l := strings.ToLower(d)
		lowerKeys[l] = append(lowerKeys[l], d)
	}
	lowerNames := make([]string, 0, len(lowerKeys))
	for l := range lowerKeys {
		lowerNames = append(lowerNames, l)
	}
	sort.Strings(lowerNames)
	for _, l := range lowerNames {
		sort.Strings(lowerKeys[l])
	}
	firstLower := ""
	if firstDomain != "" {
		firstLower = strings.ToLower(firstDomain)
	}

	emittedDefault := map[int]bool{}
	for _, actual := range effList {
		ports := effPorts[actual]
		sort.Ints(ports)
		for _, p := range ports {
			if p != actual {
				fmt.Fprintf(&b, "# ── Port %d is owned by Reality; HTTP services on it are served on the Reality HTTP port %d ──\n", p, actual)
			}
		}

		for _, lower := range lowerNames {
			// All services of this canonical domain that land on this
			// effective port, from EVERY original port that remaps here.
			seen := map[string]bool{}
			var services []string
			for _, p := range ports {
				for _, key := range lowerKeys[lower] {
					for _, name := range g.servicesForDomainPort(c, key, p) {
						if !seen[name] {
							seen[name] = true
							services = append(services, name)
						}
					}
				}
			}
			if len(services) == 0 {
				continue
			}
			sort.Strings(services)

			// Certificate for the merged block. Order matters: the key
			// spelled exactly like the hostname (lowercase) holds the
			// DOMAIN-WIDE certificate, while a case-variant duplicate
			// created by a phone's auto-capitalise usually carries a
			// certificate issued for ONE subdomain only. Picking the
			// latter for the whole block would break TLS for every other
			// subdomain in it.
			d := config.Domain{}
			if cand, ok := c.Domains[lower]; ok &&
				strings.TrimSpace(cand.Cert) != "" && strings.TrimSpace(cand.Key) != "" {
				d = cand
			} else {
				for _, key := range lowerKeys[lower] {
					cand := c.Domains[key]
					if strings.TrimSpace(cand.Cert) != "" && strings.TrimSpace(cand.Key) != "" {
						d = cand
						break
					}
				}
			}

			// A domain without a certificate (or with missing files) would
			// produce an invalid server block that fails `nginx -t` and can
			// take the whole server down on the next restart. Skip it with
			// a clear comment instead; the panel UI shows the same hint.
			if strings.TrimSpace(d.Cert) == "" || strings.TrimSpace(d.Key) == "" {
				fmt.Fprintf(&b, "# ── Domain: %s — SKIPPED: certificate or key path is empty ──\n", lower)
				fmt.Fprintf(&b, "#     Add a certificate (panel → Domains) and regenerate.\n\n")
				continue
			}

			// default_server goes to the very first block emitted for the
			// effective port.
			isFirst := lower == firstLower && !emittedDefault[actual]
			if !emittedDefault[actual] && firstLower != "" && lower != firstLower {
				// The alphabetically first domain has no service on this
				// port — the first domain that DOES gets the default flag,
				// so every port keeps exactly one default_server.
				isFirst = true
			}
			if isFirst {
				emittedDefault[actual] = true
			}
			ds := ""
			if isFirst {
				ds = " default_server"
			}
			sn := g.serverNameGroup(c, lower, lowerKeys[lower], services, isFirst)

			fmt.Fprintf(&b, "# ── Domain: %s ──\n", lower)
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
				subs := g.subsForDomainGroup(c, svcName, lower)
				b.WriteString(g.locationBlock(svcName, svc, actual, subs, lower))
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
			// EqualFold: hostnames are case-insensitive; a case-variant
			// binding must still match its domain's server block.
			if strings.EqualFold(b.Domain, domain) {
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
		if strings.EqualFold(b.Domain, domain) {
			out = append(out, b.Subdomain)
		}
	}
	return out
}

// subsForDomainGroup returns every subdomain a service is bound to under a
// canonical (lowercase) domain, regardless of how the binding spells the
// domain. Duplicates are removed.
func (g *Generator) subsForDomainGroup(c *config.Config, service, lowerDomain string) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range c.Services[service].Bindings {
		if !strings.EqualFold(b.Domain, lowerDomain) {
			continue
		}
		s := strings.ToLower(b.Subdomain)
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// serverNameGroup builds the server_name list for a canonical domain group.
// The default_server block catches the bare domain plus a wildcard; other
// blocks list only the exact FQDNs they serve, so two blocks on the same
// port can never claim the same name (which nginx reports as
// "conflicting server name ... ignored" and then drops).
func (g *Generator) serverNameGroup(c *config.Config, lower string, keys, services []string, isFirst bool) string {
	if isFirst {
		return lower + " *." + lower
	}
	names := map[string]bool{}
	for _, sv := range services {
		for _, su := range g.subsForDomainGroup(c, sv, lower) {
			if su != "" {
				names[su+"."+lower] = true
			} else {
				names[lower] = true
			}
		}
	}
	if len(names) == 0 {
		names[lower] = true
	}
	arr := make([]string, 0, len(names))
	for n := range names {
		arr = append(arr, n)
	}
	sort.Strings(arr)
	return strings.Join(arr, " ")
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
	// up is the upstream authority. It stays "127.0.0.1:<port>" for a local
	// backend (byte-identical to what the CLI panel produced) and becomes
	// "host:<port>" when the service points at another machine.
	up := fmt.Sprintf("%s:%d", config.ResolveTarget(svc.Target), lp)
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

	// Host guard — identical to the CLI panel's generator.
	var hc string
	switch {
	case len(nonEmpty) == 0:
		hc = "# no bindings"
	case len(nonEmpty) == 1:
		hc = fmt.Sprintf(`if ($host != "%s.%s") { return 302 /; }`, nonEmpty[0], domain)
	default:
		alts := make([]string, len(nonEmpty))
		for i, s := range nonEmpty {
			alts[i] = s
		}
		hc = fmt.Sprintf(`if ($host !~ "^(%s)\.(%s)$") { return 302 /; }`,
			strings.Join(alts, "|"), strings.ReplaceAll(domain, ".", "\\."))
	}

	proto := "http"
	if sb {
		proto = "https"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "    # %s → %s://%s\n", name, proto, up)

	// These five branches mirror the CLI panel's generator exactly:
	// root / path-owned services forward the FULL URI; only
	// path_owned=false services get the path-strip treatment.
	switch {
	case sp == "/" && sb:
		fmt.Fprintf(&b, "    location / {\n        %s\n", hc)
		fmt.Fprintf(&b, "        proxy_pass https://%s;\n", up)
		b.WriteString("        proxy_ssl_verify off;\n")
		b.WriteString("        proxy_ssl_server_name off;\n")
		g.writeProxyTail(&b, sb, actualPort)
		b.WriteString("    }\n")
	case sp == "/":
		fmt.Fprintf(&b, "    location / {\n        %s\n", hc)
		b.WriteString("        proxy_redirect off;\n")
		fmt.Fprintf(&b, "        proxy_pass http://%s;\n", up)
		g.writeProxyTail(&b, sb, actualPort)
		b.WriteString("    }\n")
	case po && sb:
		fmt.Fprintf(&b, "    location /%s {\n        %s\n", sp, hc)
		fmt.Fprintf(&b, "        proxy_pass https://%s;\n", up)
		b.WriteString("        proxy_ssl_verify off;\n")
		b.WriteString("        proxy_ssl_server_name off;\n")
		g.writeProxyTail(&b, sb, actualPort)
		b.WriteString("    }\n")
	case po:
		fmt.Fprintf(&b, "    location /%s {\n        %s\n", sp, hc)
		b.WriteString("        proxy_redirect off;\n")
		fmt.Fprintf(&b, "        proxy_pass http://%s;\n", up)
		g.writeProxyTail(&b, sb, actualPort)
		b.WriteString("    }\n")
	default:
		// path_owned=false → path-strip (CLI panel behaviour).
		fmt.Fprintf(&b, "    location = /%s {\n        return 301 /%s/;\n    }\n\n", sp, sp)
		fmt.Fprintf(&b, "    location /%s/ {\n        %s\n", sp, hc)
		fmt.Fprintf(&b, "        proxy_redirect http://%s/ /%s/;\n", up, sp)
		fmt.Fprintf(&b, "        proxy_redirect / /%s/;\n", sp)
		fmt.Fprintf(&b, "        proxy_cookie_path / /%s/;\n", sp)
		fmt.Fprintf(&b, "        proxy_pass http://%s/;\n", up)
		g.writeProxyTail(&b, sb, actualPort)
		b.WriteString("        sub_filter 'href=\"/' 'href=\"/" + sp + "/';\n")
		b.WriteString("        sub_filter 'src=\"/' 'src=\"/" + sp + "/';\n")
		b.WriteString("        sub_filter 'action=\"/' 'action=\"/" + sp + "/';\n")
		b.WriteString("        sub_filter '\"/api/' '\"/" + sp + "/api/';\n")
		b.WriteString("        sub_filter_once off;\n")
		b.WriteString("        sub_filter_types text/css application/javascript application/json;\n")
		b.WriteString("    }\n")
	}
	return b.String()
}

// writeProxyTail writes the shared proxy directives, in the same order the
// CLI panel's generator uses.
func (g *Generator) writeProxyTail(b *strings.Builder, sslBackend bool, actualPort int) {
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
		b.WriteString("        proxy_buffering off;\n")
	}
	b.WriteString("        proxy_read_timeout 86400s;\n")
	b.WriteString("        proxy_send_timeout 86400s;\n")
}

// ── nginx operations ────────────────────────────────────────

func (g *Generator) Test() TestResult {
	bin := NginxBinary()
	if bin == "" {
		return TestResult{OK: false, Stderr: "nginx executable not found in PATH or the usual sbin directories"}
	}
	cmd := exec.Command(bin, "-t")
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

// Reload applies the configuration to the running nginx.
//
// A RUNNING nginx is only ever reloaded — never restarted — so no
// connection is dropped. A STOPPED nginx is started instead: `systemctl
// reload` on a dead unit simply fails, which used to leave the server down
// after a reboot even though the config was valid and the panel reported
// only a generic reload error.
func (g *Generator) Reload() TestResult {
	verb := "reload"
	if !IsActive() {
		verb = "start"
		// An explicit start supersedes a previous intentional stop.
		ClearStopped()
	}
	cmd := exec.Command("systemctl", verb, "nginx")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := TestResult{
		OK:     err == nil,
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if !res.OK {
		if reason := LastFailureReason(); reason != "" {
			res.Stderr = strings.TrimSpace(res.Stderr + "\n" + reason)
		}
	}
	return res
}

// GenerateAndReload generates both config files, validates them with
// `nginx -t`, and reloads nginx — restoring the previous files on any
// failure so the running server is never left with a broken config on disk.
func (g *Generator) GenerateAndReload() (map[string]interface{}, error) {
	res := map[string]interface{}{}

	c, err := g.cfg.Read()
	if err != nil {
		return res, err
	}
	outPath := c.Nginx.OutputPath
	if outPath == "" {
		outPath = "/etc/nginx/conf.d/gateway.conf"
	}
	streamOut := c.Nginx.StreamOutputPath
	if streamOut == "" {
		streamOut = "/etc/nginx/stream-gateway.conf"
	}
	fakeDir := c.Nginx.FakeDir
	if fakeDir == "" {
		fakeDir = "/var/www/mysite"
	}

	// Snapshot everything we are about to touch: the two generated files,
	// nginx.conf (the stream include logic may edit it) and the fake-site
	// index. If anything below fails, Restore puts the previous state back.
	backup, err := SnapBackup(outPath, streamOut, g.confPath(), filepath.Join(fakeDir, "index.html"))
	if err != nil {
		return res, fmt.Errorf("backup before generate: %w", err)
	}

	paths, err := g.Generate()
	if err != nil {
		_ = backup.Restore()
		return res, err
	}
	res["paths"] = paths

	test := g.Test()
	res["test"] = test
	if !test.OK {
		_ = backup.Restore()
		res["ok"] = false
		res["restored"] = true
		return res, nil
	}

	rel := g.Reload()
	res["reload"] = rel
	if !rel.OK {
		// Reload failed — put the old files back and try to reload once more
		// so disk and the running process agree again.
		restoreErr := backup.Restore()
		retry := g.Reload()
		res["restored"] = true
		res["restore_error"] = ""
		if restoreErr != nil {
			res["restore_error"] = restoreErr.Error()
		}
		res["retry_reload"] = retry
		res["ok"] = retry.OK
		return res, nil
	}

	res["ok"] = true
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
