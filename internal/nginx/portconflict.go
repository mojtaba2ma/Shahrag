package nginx

// Port-conflict diagnosis.
//
// The reported situation was: `nginx -t` says "valid", yet nginx.service is
// inactive and will not start. `nginx -t` ONLY parses the configuration — it
// never tries to bind a single socket. So when another daemon (xray, x-ui,
// sing-box, haproxy…) already owns a port nginx is configured to listen on,
// the test passes while the actual start fails with:
//
//	nginx: [emerg] bind() to 0.0.0.0:443 failed (98: Address already in use)
//
// This file finds those conflicts BEFORE nginx is started and names the
// process that is holding each port, so the report says what to do instead
// of just "inactive".

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Listener describes one listening socket found on the host.
type Listener struct {
	Port    int
	Process string // "nginx", "xray", "shahrag", … ("" when unknown)
	PID     int
	Addr    string
}

// Conflict is a port nginx wants but another process already owns.
type Conflict struct {
	Port    int
	Process string
	PID     int
	Source  string // which generated file asks for this port
}

var reSSPort = regexp.MustCompile(`:(\d+)\s`)

// reListenDirective matches a `listen ...;` directive wherever it appears —
// at the start of a line or after `{` / `;` on a shared line.
var reListenDirective = regexp.MustCompile(`(?:^|[{;])\s*listen\s+([^;{}]+);`)

// reServerNameDirective is the same idea for `server_name ...;`.
var reServerNameDirective = regexp.MustCompile(`(?:^|[{;])\s*server_name\s+([^;{}]+);`)
var reSSUsers = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)

// Listeners returns every TCP listening socket, with the owning process when
// it can be determined (requires root for the process name, like ss does).
func Listeners() []Listener {
	out, err := exec.Command("ss", "-ltnpH").CombinedOutput()
	if err != nil {
		// -H (no header) is not supported everywhere; retry plainly.
		out, err = exec.Command("ss", "-ltnp").CombinedOutput()
		if err != nil {
			return nil
		}
	}
	var res []Listener
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// The local address column is the 4th field for `ss -ltn`.
		local := fields[3]
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(local[idx+1:])
		if err != nil {
			continue
		}
		l := Listener{Port: port, Addr: local}
		if m := reSSUsers.FindStringSubmatch(line); m != nil {
			l.Process = m[1]
			l.PID, _ = strconv.Atoi(m[2])
		}
		res = append(res, l)
	}
	return res
}

// PortsRequiredByNginx scans the generated config files (and any other file
// nginx includes from conf.d) for `listen` directives and returns the set of
// TCP ports nginx will try to bind.
func PortsRequiredByNginx(files ...string) map[int][]string {
	ports := map[int][]string{}
	// `listen` is not always at the start of a line: hand-written or
	// minified configs put several directives on one line
	// (`server { listen 443; listen [::]:443; ... }`). Anchor on a
	// statement boundary ({ ; } or line start) instead.
	reListen := reListenDirective
	for _, f := range files {
		if f == "" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range reListen.FindAllStringSubmatch(string(data), -1) {
			spec := strings.TrimSpace(m[1])
			// Strip flags: "443 ssl http2 default_server" → "443"
			spec = strings.Fields(spec)[0]
			// "[::]:443" → "443", "127.0.0.1:8081" → "8081"
			if i := strings.LastIndex(spec, "]:"); i >= 0 {
				spec = spec[i+2:]
			} else if i := strings.LastIndex(spec, ":"); i >= 0 {
				spec = spec[i+1:]
			}
			p, err := strconv.Atoi(spec)
			if err != nil || p <= 0 {
				continue
			}
			base := filepath.Base(f)
			found := false
			for _, s := range ports[p] {
				if s == base {
					found = true
				}
			}
			if !found {
				ports[p] = append(ports[p], base)
			}
		}
	}
	return ports
}

// FindPortConflicts reports every port nginx needs that is currently owned by
// a DIFFERENT process. Ports held by nginx itself are not conflicts.
//
// This is the check `nginx -t` cannot do: it explains the exact situation
// where the config is valid but the service refuses to start.
func FindPortConflicts(files ...string) []Conflict {
	required := PortsRequiredByNginx(files...)
	if len(required) == 0 {
		return nil
	}
	listeners := Listeners()

	// Map port → the first non-nginx process holding it.
	holder := map[int]Listener{}
	for _, l := range listeners {
		if strings.Contains(l.Process, "nginx") {
			continue
		}
		if _, ok := holder[l.Port]; !ok {
			holder[l.Port] = l
		}
	}

	var out []Conflict
	for port, srcs := range required {
		l, busy := holder[port]
		if !busy {
			continue
		}
		sort.Strings(srcs)
		out = append(out, Conflict{
			Port:    port,
			Process: l.Process,
			PID:     l.PID,
			Source:  strings.Join(srcs, ", "),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// DescribeConflicts renders conflicts as human-readable lines.
func DescribeConflicts(cs []Conflict) []string {
	var lines []string
	for _, c := range cs {
		who := c.Process
		if who == "" {
			who = "an unknown process"
		}
		if c.PID > 0 {
			who = fmt.Sprintf("%s (pid %d)", who, c.PID)
		}
		lines = append(lines,
			fmt.Sprintf("port %d is required by %s but is already held by %s",
				c.Port, c.Source, who))
	}
	return lines
}

// GeneratedFiles returns the config files Shahrag generates plus every
// *.conf in conf.d, which is what nginx actually loads.
func GeneratedFiles(gateway, stream string) []string {
	files := []string{}
	// Resolve to canonical absolute paths so the SAME file reached through
	// two paths (a symlink, or conf.d/gateway.conf given explicitly AND
	// found by the directory scan) is never counted twice — that would
	// report a phantom "duplicate server name" against itself.
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" {
			return
		}
		key := p
		if abs, err := filepath.Abs(p); err == nil {
			if resolved, err2 := filepath.EvalSymlinks(abs); err2 == nil {
				key = resolved
			} else {
				key = abs
			}
		}
		if seen[key] {
			return
		}
		seen[key] = true
		files = append(files, p)
	}
	add(gateway)
	add(stream)
	// Scan every directory nginx loads drop-ins from, so a leftover file
	// from an older setup is found and named — that is the only place a
	// duplicate server_name can still come from.
	dirs := []string{envOrDefault("SHAHRAG_CONFD_DIR", confDDir), "/etc/nginx/sites-enabled"}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if d == confDDir && !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			add(filepath.Join(d, e.Name()))
		}
	}
	return files
}

// ── Foreign server blocks ───────────────────────────────────
//
// Shahrag's generated files claim only FQDNs (sub.domain.tld) and, for the
// default block, "domain *.domain". A warning like
//
//	conflicting server name "sugerdood.com" on 0.0.0.0:6038, ignored
//
// that names a hostname Shahrag does NOT emit therefore proves a leftover
// config file from an older setup is still being loaded — nginx keeps the
// first block claiming a name and IGNORES the rest, so whichever block
// loses becomes dead weight (its services serve the fake page).

// ServerNameClaim is one server_name token claimed by one file.
type ServerNameClaim struct {
	Name string
	Port int
	File string
}

var reServerName = reServerNameDirective

// ScanServerNames parses the given files and returns every (port, name)
// claim, so duplicates across files can be attributed to their source.
func ScanServerNames(files ...string) []ServerNameClaim {
	var claims []ServerNameClaim
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		// Walk server blocks so names can be tied to the block's listen ports.
		for _, block := range splitServerBlocks(string(data)) {
			var ports []int
			for _, m := range reListenDirective.FindAllStringSubmatch(block, -1) {
				spec := strings.Fields(strings.TrimSpace(m[1]))[0]
				if i := strings.LastIndex(spec, "]:"); i >= 0 {
					spec = spec[i+2:]
				} else if i := strings.LastIndex(spec, ":"); i >= 0 {
					spec = spec[i+1:]
				}
				if p, err := strconv.Atoi(spec); err == nil {
					dup := false
					for _, e := range ports {
						if e == p {
							dup = true
						}
					}
					if !dup {
						ports = append(ports, p)
					}
				}
			}
			for _, m := range reServerName.FindAllStringSubmatch(block, -1) {
				for _, n := range strings.Fields(m[1]) {
					if n == "_" {
						continue
					}
					for _, p := range ports {
						claims = append(claims, ServerNameClaim{Name: n, Port: p, File: f})
					}
				}
			}
		}
	}
	return claims
}

// splitServerBlocks returns the text of each top-level `server { ... }` block.
func splitServerBlocks(txt string) []string {
	var blocks []string
	for i := 0; i < len(txt); {
		idx := strings.Index(txt[i:], "server")
		if idx < 0 {
			break
		}
		start := i + idx
		brace := strings.Index(txt[start:], "{")
		if brace < 0 {
			break
		}
		depth, j := 0, start+brace
		for ; j < len(txt); j++ {
			switch txt[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if j >= len(txt) {
			break
		}
		blocks = append(blocks, txt[start:j+1])
		i = j + 1
	}
	return blocks
}

// DuplicateServerNames returns the (port, name) pairs claimed by more than
// one server block, with the files involved — exactly what nginx warns about.
func DuplicateServerNames(files ...string) map[string][]string {
	type key struct {
		name string
		port int
	}
	seen := map[key][]string{}
	for _, c := range ScanServerNames(files...) {
		k := key{c.Name, c.Port}
		seen[k] = append(seen[k], c.File)
	}
	out := map[string][]string{}
	for k, fs := range seen {
		if len(fs) < 2 {
			continue
		}
		out[fmt.Sprintf("%s on port %d", k.name, k.port)] = fs
	}
	return out
}

// ── Attributing a conflicting port to its owner in the config ──

// RealityPortOwners maps each Reality listen port to the Reality service
// names that requested it. A port conflict on such a port is fixed by
// editing (or deleting) that Reality service — knowing its NAME turns an
// opaque "port 8443 is busy" into a one-step fix.
func RealityPortOwners(reality map[string][]int) map[int][]string {
	out := map[int][]string{}
	for name, ports := range reality {
		for _, p := range ports {
			out[p] = append(out[p], name)
		}
	}
	for p := range out {
		sort.Strings(out[p])
	}
	return out
}

// LocalIPv4 returns every non-loopback IPv4 address of this machine. Used to
// recognise "the resolver answered with US", which is the loop condition for
// pass-through routing.
func LocalIPv4() []string {
	var out []string
	seen := map[string]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() {
				continue
			}
			s := ipnet.IP.String()
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}
