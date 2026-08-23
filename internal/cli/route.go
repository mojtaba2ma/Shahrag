package cli

// `shahrag route <domain>` — answer "what happens to this domain?" end to end.
//
// Splitting a game's traffic (login through the relay, voice/CDN direct) means
// constantly asking three questions about one hostname:
//
//	1. does the panel have an SNI rule for it, and where does that rule send it?
//	2. what address does DNS actually hand a client?
//	3. does a TLS connection through the relay really reach the real site?
//
// Guessing any of these wastes hours, so this command answers all three.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

// RunRoute is `shahrag route <domain> [more domains...]`.
func RunRoute(domains []string) int {
	if len(domains) == 0 {
		fmt.Println("Usage: shahrag route <domain> [domain...]")
		fmt.Println()
		fmt.Println("Shows, for each domain:")
		fmt.Println("  • which SNI rule matches it (and where that rule sends it)")
		fmt.Println("  • what this server's DNS answers, and what the truth is")
		fmt.Println("  • whether a real TLS connection through the relay succeeds")
		return 1
	}

	cfg := config.New()
	c, err := cfg.Read()
	if err != nil {
		fmt.Printf("cannot read config: %v\n", err)
		return 1
	}

	fmt.Printf("Shahrag v%s — route report\n", version)
	fmt.Println("════════════════════════════════════════════")

	myIPs := localAddresses()
	if len(myIPs) > 0 {
		fmt.Printf("this server: %s\n", strings.Join(myIPs, ", "))
	}
	fmt.Println()

	failed := 0
	for _, d := range domains {
		if !routeOne(c, strings.TrimSpace(strings.ToLower(d)), myIPs) {
			failed++
		}
		fmt.Println()
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func routeOne(c *config.Config, domain string, myIPs []string) bool {
	fmt.Printf("── %s ──\n", domain)
	ok := true

	// 1. Which SNI rule matches?
	name, svc, matched := matchSNIRule(c, domain)
	switch {
	case !matched:
		fmt.Printf("  rule      : %s — traffic arriving with this SNI falls to the default\n", yellow("none"))
		fmt.Printf("              (the Reality HTTP port %d)\n", c.Reality.HTTPPort)
	case strings.TrimSpace(svc.Target) == config.PassthroughTarget:
		fmt.Printf("  rule      : %s (SNI %q)\n", green(name), svc.SNI)
		fmt.Printf("  action    : pass through to the REAL site on port %d\n", portOr(svc.LocalPort, 443))
	case config.IsLocalTarget(svc.Target):
		fmt.Printf("  rule      : %s (SNI %q)\n", green(name), svc.SNI)
		fmt.Printf("  action    : local backend 127.0.0.1:%d\n", svc.LocalPort)
	default:
		fmt.Printf("  rule      : %s (SNI %q)\n", green(name), svc.SNI)
		fmt.Printf("  action    : forward to %s:%d\n", svc.Target, portOr(svc.LocalPort, 443))
	}

	// 2. What does DNS say?
	local := resolveWith(domain, "127.0.0.1:53")
	truth := resolveWith(domain, firstResolver(c))

	if len(local) > 0 {
		via := "→ this server (relayed)"
		if !anyIn(local, myIPs) {
			via = "→ direct to the site"
		}
		fmt.Printf("  this DNS  : %s  %s\n", strings.Join(local, ", "), via)
	} else {
		fmt.Printf("  this DNS  : %s (no local DNS on port 53, or no answer)\n", dim("—"))
	}
	if len(truth) > 0 {
		fmt.Printf("  real IP   : %s\n", strings.Join(truth, ", "))
	} else {
		fmt.Printf("  real IP   : %s could not resolve via %s\n", red("FAILED"), firstResolver(c))
		ok = false
	}

	// The dangerous combination: the panel relays this domain, but the
	// resolver nginx uses also points back here.
	if matched && strings.TrimSpace(svc.Target) == config.PassthroughTarget {
		if anyIn(truth, myIPs) {
			fmt.Printf("  %s the resolver used for pass-through returns THIS server.\n", red("LOOP RISK:"))
			fmt.Printf("            nginx would connect to itself. Point the panel's resolver at\n")
			fmt.Printf("            a truth resolver (Unbound on 127.0.0.1:5335) or 1.1.1.1.\n")
			ok = false
		}
	}

	// 3. Does a real connection work?
	if matched && strings.TrimSpace(svc.Target) == config.PassthroughTarget {
		port := portOr(svc.LocalPort, 443)
		target := "127.0.0.1"
		if len(myIPs) > 0 {
			target = myIPs[0]
		}
		if err := tlsProbe(target, port, domain); err != nil {
			fmt.Printf("  live test : %s %v\n", red("FAILED"), err)
			ok = false
		} else {
			fmt.Printf("  live test : %s TLS handshake through the relay succeeded\n", green("OK"))
		}
	}
	return ok
}

// matchSNIRule finds the SNI rule that would match this hostname, mirroring
// how nginx evaluates the generated map (wildcards become regexes).
func matchSNIRule(c *config.Config, domain string) (string, config.RealityService, bool) {
	names := make([]string, 0, len(c.Reality.Services))
	for n := range c.Reality.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		svc := c.Reality.Services[n]
		if sniMatches(svc.SNI, domain) {
			return n, svc, true
		}
	}
	return "", config.RealityService{}, false
}

func sniMatches(pattern, host string) bool {
	pattern = strings.TrimSpace(strings.ToLower(pattern))
	host = strings.ToLower(host)
	switch {
	case pattern == "":
		return false
	case strings.HasPrefix(pattern, "~"):
		expr := strings.TrimPrefix(strings.TrimPrefix(pattern, "~*"), "~")
		re, err := regexp.Compile("(?i)" + expr)
		return err == nil && re.MatchString(host)
	case strings.HasPrefix(pattern, "*."):
		base := strings.TrimPrefix(pattern, "*.")
		return host == base || strings.HasSuffix(host, "."+base)
	default:
		return host == pattern
	}
}

// resolveWith queries a specific resolver using the system tools that exist
// on a minimal server (no extra dependencies).
func resolveWith(domain, server string) []string {
	host, port := server, "53"
	if h, p, err := net.SplitHostPort(server); err == nil {
		host, port = h, p
	}
	if path, err := exec.LookPath("dig"); err == nil {
		out, _ := exec.Command(path, "@"+host, "-p", port, domain, "+short",
			"+time=3", "+tries=1").Output()
		return ipsFrom(string(out))
	}
	// No dig available: query the chosen server directly with Go's resolver
	// so the answer really comes from THAT server, not from /etc/resolv.conf.
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", net.JoinHostPort(host, port))
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	addrs, err := r.LookupHost(ctx, domain)
	if err != nil {
		return nil
	}
	return ipsFrom(strings.Join(addrs, "\n"))
}

var reIP = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)

func ipsFrom(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if reIP.MatchString(l) {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// tlsProbe opens a real TLS connection with the given SNI and reports whether
// the handshake completed — the only proof that the relay actually works.
func tlsProbe(addr string, port int, sni string) error {
	d := net.Dialer{Timeout: 8 * time.Second}
	conn, err := tls.DialWithDialer(&d, "tcp", net.JoinHostPort(addr, fmt.Sprint(port)),
		&tls.Config{ServerName: sni, InsecureSkipVerify: true})
	if err != nil {
		return err
	}
	defer conn.Close()
	st := conn.ConnectionState()
	if len(st.PeerCertificates) > 0 {
		fmt.Printf("              certificate CN: %s\n", st.PeerCertificates[0].Subject.CommonName)
	}
	return nil
}

func firstResolver(c *config.Config) string {
	list := c.Reality.Resolvers
	if len(list) == 0 {
		list = config.DefaultResolvers()
	}
	s := strings.TrimSpace(list[0])
	if _, _, err := net.SplitHostPort(s); err != nil {
		s = net.JoinHostPort(s, "53")
	}
	return s
}

// localAddresses returns this machine's non-loopback IPv4 addresses plus its
// public address when it can be determined cheaply.
func localAddresses() []string {
	var out []string
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
				out = append(out, ipnet.IP.String())
			}
		}
	}
	if o, err := exec.Command("hostname", "-I").Output(); err == nil {
		for _, f := range strings.Fields(string(o)) {
			if reIP.MatchString(f) && !contains(out, f) {
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func anyIn(list, set []string) bool {
	for _, l := range list {
		if contains(set, l) {
			return true
		}
	}
	return false
}

func portOr(p, def int) int {
	if p > 0 {
		return p
	}
	return def
}

func dim(s string) string { return "\033[2m" + s + "\033[0m" }

// Keep nginxpkg referenced for future use in this file.
var _ = nginxpkg.NginxBinary
