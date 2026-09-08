package cli

// Health and topology in the terminal.
//
// The reason this exists is narrow and important: when the web panel is
// unreachable, these are the two views an operator needs most, and that is
// exactly the moment the web panel cannot show them. A summary over SSH is
// not a nicety, it is the fallback path.
//
// So it is deliberately a SUMMARY, not a port of the web page. No charts, no
// history, no polling — the questions being answered are "is anything
// obviously wrong?" and "what is listening where?", and both fit on one
// screen.
//
// It reads the same internal/health Report and the same topology derivation
// the panel and the Telegram bot use, so the three can never disagree about
// whether the server is healthy.

import (
	"fmt"
	"sort"
	"strings"

	"shahrag/internal/certs"
	"shahrag/internal/config"
	"shahrag/internal/health"
	nginxpkg "shahrag/internal/nginx"
)

// BuildTag identifies the running build. Assigned by main, the same way
// web.BuildTag is, because the constant lives in the main package.
var BuildTag = "dev"

// ANSI colours. Kept minimal and always paired with a WORD, never colour
// alone: a red dot means nothing over a monochrome terminal, through a pipe,
// or to somebody who cannot distinguish red from green.
const (
	cReset = "\033[0m"
	cGreen = "\033[32m"
	cAmber = "\033[33m"
	cRed   = "\033[31m"
	cDim   = "\033[2m"
	cBold  = "\033[1m"
)

func levelColour(l health.Level) string {
	switch l {
	case health.LevelBad:
		return cRed
	case health.LevelWarn:
		return cAmber
	}
	return cGreen
}

func levelWord(l health.Level) string {
	switch l {
	case health.LevelBad:
		return "FAIL"
	case health.LevelWarn:
		return "WARN"
	}
	return "OK  "
}

// buildCollector wires a health collector with the same probes the web
// server uses, so the CLI and the panel report identically.
func buildCollector(cfg *config.Manager, gen *nginxpkg.Generator) *health.Collector {
	return health.NewCollector(health.Deps{
		// The build TAG, not the product version: 1.0.0 is fixed for
		// ever and tells nobody which build is running, which is the
		// only thing this field is for.
		Build:          BuildTag,
		NginxActive:    nginxpkg.IsActive,
		NginxEnabled:   nginxpkg.IsEnabled,
		NginxVersion:   nginxpkg.Version,
		NginxWorkerCon: nginxpkg.WorkerConnections,
		NginxFailure:   nginxpkg.LastFailureReason,
		NginxConfTest: func() (bool, string) {
			r := gen.Test()
			return r.OK, r.Stderr
		},
		HoneypotOn: func() (bool, string) {
			c, err := cfg.Read()
			if err != nil || c == nil {
				return false, ""
			}
			return c.Honeypot.Enabled, c.Honeypot.EffectiveMode()
		},
		AutoBanOn: func() (bool, string) {
			c, err := cfg.Read()
			if err != nil || c == nil {
				return false, ""
			}
			return c.AutoBan.Enabled && c.AutoBan.AnyRuleEnabled(), c.AutoBan.EffectiveAction()
		},
		// The ban ENGINE is not running in the CLI — it lives in the
		// serve process. Reading the persisted list is the honest
		// alternative: it reports what nginx is actually enforcing right
		// now, which is what the operator is asking about.
		BanCounts: func() (int, int, bool) {
			e := health.PersistedBanCount()
			return e, 0, false
		},
		CertExpiry: func() health.Certs { return cliCertHealth(cfg) },
	})
}

func cliCertHealth(cfg *config.Manager) health.Certs {
	c, err := cfg.Read()
	if err != nil || c == nil {
		return health.Certs{}
	}
	out := health.Certs{SoonestDays: 1 << 30}
	list := make([]certs.Info, 0, len(c.Domains))
	for name, d := range c.Domains {
		if d.Cert == "" && d.Key == "" {
			continue
		}
		in := certs.Inspect(name, d.Cert, d.Key)
		list = append(list, in)
		if in.Error == "" && !in.Expired && in.DaysLeft < out.SoonestDays {
			out.SoonestDays, out.SoonestName = in.DaysLeft, name
		}
	}
	sum := certs.Summarise(list)
	out.Total, out.Expired = sum.Total, sum.Expired
	out.DueSoon, out.Problems = sum.DueSoon, sum.Problems
	if out.SoonestDays == 1<<30 {
		out.SoonestDays = 0
	}
	return out
}

// PrintHealth writes the health summary.
//
// Takes two samples with a short gap, because every rate in the report —
// CPU, and above all the swap in/out rate — is the difference between two
// readings. A single-shot version would have to print "not measured yet" for
// the one number the operator most wants, so it waits the second instead.
func PrintHealth(cfg *config.Manager, gen *nginxpkg.Generator) int {
	col := buildCollector(cfg, gen)
	col.Collect() // prime: establishes the baseline the rates subtract from
	health.SleepForRate()
	r := col.Collect()

	fmt.Printf("\n%s%s Shahrag health%s   %s%s%s\n",
		cBold, "▌", cReset, levelColour(r.Level), strings.ToUpper(string(r.Level)), cReset)
	fmt.Printf("%s%s · kernel %s · up %s%s\n\n",
		cDim, r.Host, r.Kernel, humanDur(r.UptimeSec), cReset)

	for _, ch := range health.SortChecks(r.Checks) {
		col := levelColour(ch.Level)
		fmt.Printf("  %s%s%s  %-12s %-14s %s%s%s\n",
			col, levelWord(ch.Level), cReset,
			ch.ID, ch.Value, cDim, ch.Detail, cReset)
	}

	// Swap gets its own paragraph, because the gauge is the number people
	// misread and the rate is the one that matters. Saying it in words
	// here is the whole point of the section.
	fmt.Println()
	if r.Swap.TotalBytes == 0 {
		fmt.Printf("  swap:  none configured\n")
	} else {
		fmt.Printf("  swap:  %s of %s used (%.0f%%)\n",
			humanBytes(r.Swap.UsedBytes), humanBytes(r.Swap.TotalBytes), r.Swap.UsedPct)
		if !r.Swap.Measured {
			fmt.Printf("         rate not measured\n")
		} else if r.Swap.Active {
			fmt.Printf("         %sACTIVE: in %.0f p/s, out %.0f p/s — the server IS swapping%s\n",
				cAmber, r.Swap.InPagesPerSec, r.Swap.OutPagesPerSec, cReset)
		} else {
			fmt.Printf("         %sidle: nothing is being paged, so the %.0f%% above is\n"+
				"         history, not a problem%s\n",
				cGreen, r.Swap.UsedPct, cReset)
		}
	}

	fmt.Printf("\n  memory: %s of %s (%.0f%%), %s cache\n",
		humanBytes(r.Memory.UsedBytes), humanBytes(r.Memory.TotalBytes),
		r.Memory.UsedPct, humanBytes(r.Memory.CacheBytes))
	fmt.Printf("  cpu:    %.0f%% · load %.2f / %.2f / %.2f on %d cores\n",
		r.CPU.UsedPct, r.CPU.Load1, r.CPU.Load5, r.CPU.Load15, r.CPU.Cores)
	if r.CPU.IOWaitPct >= 1 {
		fmt.Printf("          iowait %.1f%%\n", r.CPU.IOWaitPct)
	}
	if r.CPU.StealPct >= 1 {
		fmt.Printf("          steal %.1f%% (the hypervisor, not your server)\n", r.CPU.StealPct)
	}
	fmt.Printf("  disk:   %.0f%% used, %s free, inodes %.0f%%\n",
		r.Disk.UsedPct, humanBytes(r.Disk.FreeBytes), r.Disk.InodesPct)
	fmt.Printf("  panel:  %s heap · %d goroutines · %d fds · build %s\n\n",
		humanBytes(r.Panel.RssAnonBytes), r.Panel.Goroutines, r.Panel.OpenFDs, r.Panel.Build)

	if r.Level == health.LevelBad {
		return 1
	}
	return 0
}

// PrintMap writes the routing summary: what listens where, and where it
// goes. The terminal version of the panel's topology map.
func PrintMap(cfg *config.Manager) int {
	c, err := cfg.Read()
	if err != nil {
		fmt.Printf("cannot read the configuration: %v\n", err)
		return 1
	}

	fmt.Printf("\n%s%s Routing map%s\n\n", cBold, "▌", cReset)

	// ── Listening ports ──────────────────────────────────────
	type portRow struct {
		port     int
		kind     string
		services []string
		sni      []string
	}
	rows := map[int]*portRow{}
	touch := func(p int, kind string) *portRow {
		if p <= 0 {
			return nil
		}
		r := rows[p]
		if r == nil {
			r = &portRow{port: p, kind: kind}
			rows[p] = r
		}
		return r
	}

	if c.Reality.Enabled {
		for name, rs := range c.Reality.Services {
			if rs.Disabled {
				continue
			}
			for _, p := range rs.Ports {
				r := touch(p, "sni")
				r.kind = "sni"
				r.services = append(r.services, name)
				if rs.SNI != "" {
					r.sni = append(r.sni, rs.SNI)
				}
			}
		}
	}
	for _, p := range c.ListenPorts {
		if rows[p] == nil {
			touch(p, "https")
		}
	}
	for name, svc := range c.Services {
		if !svc.IsEnabled() {
			continue
		}
		p := svc.ListenPort
		if p <= 0 {
			p = 443
		}
		if r := rows[p]; r != nil && r.kind == "sni" {
			continue // owned by the stream module
		}
		if r := touch(p, "https"); r != nil {
			r.services = append(r.services, name)
		}
	}

	ports := make([]int, 0, len(rows))
	for p := range rows {
		ports = append(ports, p)
	}
	sort.Ints(ports)

	fmt.Printf("  %sPORTS%s\n", cBold, cReset)
	for _, p := range ports {
		r := rows[p]
		sort.Strings(r.services)
		kind := "HTTPS"
		if r.kind == "sni" {
			kind = "SNI  "
		}
		fmt.Printf("    :%-6d %s  %s\n", p, kind, strings.Join(r.services, ", "))
		if len(r.sni) > 0 {
			sort.Strings(r.sni)
			fmt.Printf("             %ssplit on: %s%s\n", cDim, strings.Join(r.sni, ", "), cReset)
		}
	}
	if c.Reality.Enabled && c.Reality.HTTPPort > 0 {
		fmt.Printf("    :%-6d %s  %sfallback for anything that matches no SNI name%s\n",
			c.Reality.HTTPPort, "HTTP ", cDim, cReset)
	}

	// ── Services ─────────────────────────────────────────────
	names := make([]string, 0, len(c.Services))
	for n := range c.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Printf("\n  %sSERVICES%s\n", cBold, cReset)
	panelName := c.Shahrag.Panel.ServiceName
	for _, n := range names {
		svc := c.Services[n]
		state := cGreen + "on " + cReset
		if !svc.IsEnabled() {
			state = cDim + "off" + cReset
		}
		target := svc.Target
		switch target {
		case "", "localhost", "127.0.0.1":
			target = "127.0.0.1"
		case config.PassthroughTarget:
			target = "passthrough"
		}
		tag := ""
		if n == panelName {
			tag = cAmber + " [panel]" + cReset
		}
		if svc.IPRestricted() {
			tag += cDim + " [ip-locked]" + cReset
		}
		if svc.GateEnabled() {
			tag += cDim + " [shield]" + cReset
		}
		fmt.Printf("    %s %-18s :%-6d -> %s:%d%s\n",
			state, truncate(n, 18), svcPort(svc), target, svc.LocalPort, tag)

		path := "/" + strings.TrimPrefix(svc.Path, "/")
		for _, b := range svc.Bindings {
			fqdn := b.Domain
			if b.Subdomain != "" {
				fqdn = b.Subdomain + "." + b.Domain
			}
			cert := ""
			if d, ok := c.Domains[b.Domain]; ok && d.Cert != "" {
				cert = cGreen + " (cert)" + cReset
			} else {
				cert = cAmber + " (no cert)" + cReset
			}
			fmt.Printf("        %shttps://%s%s%s%s\n", cDim, fqdn, path, cReset, cert)
		}
		if len(svc.Bindings) == 0 {
			fmt.Printf("        %sno domain bound%s\n", cDim, cReset)
		}
	}

	// ── SNI services ─────────────────────────────────────────
	if c.Reality.Enabled && len(c.Reality.Services) > 0 {
		rn := make([]string, 0, len(c.Reality.Services))
		for n := range c.Reality.Services {
			rn = append(rn, n)
		}
		sort.Strings(rn)
		fmt.Printf("\n  %sSNI ROUTES%s\n", cBold, cReset)
		for _, n := range rn {
			rs := c.Reality.Services[n]
			state := cGreen + "on " + cReset
			if rs.Disabled {
				state = cDim + "off" + cReset
			}
			target := rs.Target
			if target == config.PassthroughTarget {
				target = "passthrough"
			} else if target == "" {
				target = "127.0.0.1"
			}
			fmt.Printf("    %s %-18s %-28s -> %s:%d\n",
				state, truncate(n, 18), truncate(rs.SNI, 28), target, rs.LocalPort)
		}
	}

	// ── Protection ───────────────────────────────────────────
	fmt.Printf("\n  %sPROTECTION%s\n", cBold, cReset)
	if c.Honeypot.Enabled {
		fmt.Printf("    honeypot   %son%s  mode=%s  %d bait paths\n",
			cGreen, cReset, c.Honeypot.EffectiveMode(), len(c.Honeypot.EffectivePaths()))
	} else {
		fmt.Printf("    honeypot   %soff%s\n", cDim, cReset)
	}
	if c.AutoBan.Enabled && c.AutoBan.AnyRuleEnabled() {
		fmt.Printf("    auto-ban   %son%s  action=%s  %d currently banned\n",
			cGreen, cReset, c.AutoBan.EffectiveAction(), health.PersistedBanCount())
	} else {
		fmt.Printf("    auto-ban   %soff%s\n", cDim, cReset)
	}
	if c.NginxSettings.Tuning.Enabled {
		fmt.Printf("    tuning     %son%s  %s\n", cGreen, cReset,
			nginxpkg.MainTuningSummary(c))
	} else {
		fmt.Printf("    tuning     %soff%s\n", cDim, cReset)
	}
	fmt.Println()
	return 0
}

// svcPort is the port a service listens on, defaulting the way the
// generator does when none is configured.
func svcPort(s config.Service) int {
	if s.ListenPort > 0 {
		return s.ListenPort
	}
	return 443
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func humanBytes(b int64) string {
	if b < 0 {
		b = 0
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	s := [...]string{"KB", "MB", "GB", "TB"}[exp]
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, s)
	}
	return fmt.Sprintf("%.1f %s", v, s)
}

func humanDur(sec int64) string {
	d := sec / 86400
	h := (sec % 86400) / 3600
	m := (sec % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// RunHealth is the `shahrag health` entry point.
func RunHealth() int {
	cfg := config.New()
	gen := nginxpkg.NewGenerator(cfg)
	return PrintHealth(cfg, gen)
}

// RunMap is the `shahrag map` entry point.
func RunMap() int {
	return PrintMap(config.New())
}
