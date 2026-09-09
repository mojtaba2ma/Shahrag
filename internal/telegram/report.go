package telegram

// Rendering the panel's data as plain text.
//
// Every one of these reads exactly the same source the web panel reads —
// internal/health's Report, and the same topology derivation — so the bot
// and the page can never disagree about whether the server is healthy.
//
// The formatting rules come from where this is read: a phone, in a chat, in
// a code block. That means a fixed narrow width, no colour, and the worst
// news first.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/health"
)

// PanelReporter implements Reporter from the panel's own components.
type PanelReporter struct {
	Cfg    *config.Manager
	Health *health.Collector
	// Bans returns the currently banned addresses with a reason. Supplied
	// as a func so this package does not import banner, which would drag
	// the whole ban engine into the bot's dependency graph.
	Bans func() []BanLine
}

// BanLine is one banned address, flattened for display.
type BanLine struct {
	IP        string
	Reason    string
	Remaining int
	Permanent bool
}

// mark is the one-character status flag.
//
// A CHARACTER, not a colour: this is read in a chat where there is no
// colour, and on a phone where an emoji may not render. "!" and "x" survive
// every client.
func mark(l health.Level) string {
	switch l {
	case health.LevelBad:
		return "x"
	case health.LevelWarn:
		return "!"
	}
	return "+"
}

// HealthText renders the health summary.
func (p *PanelReporter) HealthText() string {
	if p.Health == nil {
		return "health is not available"
	}
	r := p.Health.Collect()

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", strings.ToUpper(string(r.Level)), r.Host)
	fmt.Fprintf(&b, "up %s\n\n", shortDur(r.UptimeSec))

	// Worst first: on a phone the first three lines are all that is read
	// before the operator decides whether to open the panel.
	for _, ch := range health.SortChecks(r.Checks) {
		line := fmt.Sprintf("%s %-11s %s", mark(ch.Level), ch.ID, ch.Value)
		if ch.Detail != "" {
			line += "  " + ch.Detail
		}
		b.WriteString(clip(line, 60) + "\n")
	}

	// Swap gets its own paragraph for the same reason it does everywhere
	// else in this project: the gauge is the number people misread, and
	// the rate is the one that matters.
	b.WriteString("\n")
	switch {
	case r.Swap.TotalBytes == 0:
		b.WriteString("swap: none configured\n")
	case !r.Swap.Measured:
		fmt.Fprintf(&b, "swap: %s of %s used, rate not measured yet\n",
			hb(r.Swap.UsedBytes), hb(r.Swap.TotalBytes))
	case r.Swap.Active:
		fmt.Fprintf(&b, "swap: ACTIVE, in %.0f out %.0f pages/s\n",
			r.Swap.InPagesPerSec, r.Swap.OutPagesPerSec)
	default:
		fmt.Fprintf(&b, "swap: %.0f%% used but IDLE — nothing is being\n"+
			"      paged, so that figure is history, not a problem\n", r.Swap.UsedPct)
	}

	fmt.Fprintf(&b, "\nmem  %s of %s (%.0f%%)\n",
		hb(r.Memory.UsedBytes), hb(r.Memory.TotalBytes), r.Memory.UsedPct)
	fmt.Fprintf(&b, "cpu  %.0f%%  load %.2f on %d cores\n",
		r.CPU.UsedPct, r.CPU.Load1, r.CPU.Cores)
	fmt.Fprintf(&b, "disk %.0f%%  %s free\n", r.Disk.UsedPct, hb(r.Disk.FreeBytes))
	fmt.Fprintf(&b, "panel %s  build %s\n", hb(r.Panel.RssAnonBytes), r.Panel.Build)
	return b.String()
}

// MapText renders the routing.
func (p *PanelReporter) MapText() string {
	c, err := p.Cfg.Read()
	if err != nil || c == nil {
		return "cannot read the configuration"
	}

	var b strings.Builder
	b.WriteString("PORTS\n")

	type row struct {
		kind string
		svc  []string
		sni  []string
	}
	rows := map[int]*row{}
	touch := func(port int, kind string) *row {
		if port <= 0 {
			return nil
		}
		r := rows[port]
		if r == nil {
			r = &row{kind: kind}
			rows[port] = r
		}
		return r
	}
	if c.Reality.Enabled {
		for name, rs := range c.Reality.Services {
			if rs.Disabled {
				continue
			}
			for _, port := range rs.Ports {
				r := touch(port, "sni")
				r.kind = "sni"
				r.svc = append(r.svc, name)
				if rs.SNI != "" {
					r.sni = append(r.sni, rs.SNI)
				}
			}
		}
	}
	for _, port := range c.ListenPorts {
		touch(port, "https")
	}
	for name, svc := range c.Services {
		if !svc.IsEnabled() {
			continue
		}
		port := svc.ListenPort
		if port <= 0 {
			port = 443
		}
		if r := rows[port]; r != nil && r.kind == "sni" {
			continue
		}
		if r := touch(port, "https"); r != nil {
			r.svc = append(r.svc, name)
		}
	}

	ports := make([]int, 0, len(rows))
	for port := range rows {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	for _, port := range ports {
		r := rows[port]
		sort.Strings(r.svc)
		kind := "HTTP "
		if r.kind == "sni" {
			kind = "SNI  "
		}
		fmt.Fprintf(&b, "  %-6d %s %s\n", port, kind,
			clip(strings.Join(r.svc, ", "), 34))
		if len(r.sni) > 0 {
			sort.Strings(r.sni)
			fmt.Fprintf(&b, "         %s\n", clip(strings.Join(r.sni, ", "), 44))
		}
	}
	if c.Reality.Enabled && c.Reality.HTTPPort > 0 {
		fmt.Fprintf(&b, "  %-6d fallback for unmatched SNI\n", c.Reality.HTTPPort)
	}

	b.WriteString("\nSERVICES\n")
	names := make([]string, 0, len(c.Services))
	for n := range c.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	panelName := c.Shahrag.Panel.ServiceName
	for _, n := range names {
		svc := c.Services[n]
		state := "on "
		if !svc.IsEnabled() {
			state = "off"
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
			tag = " [panel]"
		}
		fmt.Fprintf(&b, "  %s %-16s -> %s:%d%s\n",
			state, clip(n, 16), target, svc.LocalPort, tag)
		for _, bd := range svc.Bindings {
			f := bd.Domain
			if bd.Subdomain != "" {
				f = bd.Subdomain + "." + bd.Domain
			}
			fmt.Fprintf(&b, "      %s\n",
				clip(f+"/"+strings.TrimPrefix(svc.Path, "/"), 52))
		}
	}
	return b.String()
}

// ServicesText lists services and their state.
func (p *PanelReporter) ServicesText() string {
	c, err := p.Cfg.Read()
	if err != nil || c == nil {
		return "cannot read the configuration"
	}
	names := make([]string, 0, len(c.Services))
	for n := range c.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "no services are configured"
	}

	var b strings.Builder
	on := 0
	for _, n := range names {
		if c.Services[n].IsEnabled() {
			on++
		}
	}
	fmt.Fprintf(&b, "%d services, %d on\n\n", len(names), on)
	for _, n := range names {
		svc := c.Services[n]
		state := "on "
		if !svc.IsEnabled() {
			state = "off"
		}
		flags := ""
		if svc.IPRestricted() {
			flags += " ip-locked"
		}
		if svc.GateEnabled() {
			flags += " shield"
		}
		port := svc.ListenPort
		if port <= 0 {
			port = 443
		}
		fmt.Fprintf(&b, "%s %-16s %-6d -> %d%s\n",
			state, clip(n, 16), port, svc.LocalPort, flags)
	}
	return b.String()
}

// BansText lists what is blocked right now.
func (p *PanelReporter) BansText() string {
	if p.Bans == nil {
		return "the ban engine is not running"
	}
	list := p.Bans()
	if len(list) == 0 {
		return "nothing is banned right now"
	}
	sort.Slice(list, func(i, j int) bool { return list[i].IP < list[j].IP })

	var b strings.Builder
	fmt.Fprintf(&b, "%d banned\n\n", len(list))
	// Bounded: a server under a scan can have thousands, and a chat
	// message is not the place to enumerate them.
	shown := list
	if len(shown) > 40 {
		shown = shown[:40]
	}
	for _, x := range shown {
		left := fmt.Sprintf("%dm", x.Remaining)
		if x.Permanent {
			left = "forever"
		} else if x.Remaining >= 60 {
			left = fmt.Sprintf("%dh", x.Remaining/60)
		}
		fmt.Fprintf(&b, "%-16s %-11s %s\n", x.IP, clip(x.Reason, 11), left)
	}
	if len(list) > len(shown) {
		fmt.Fprintf(&b, "\n... and %d more\n", len(list)-len(shown))
	}
	return b.String()
}

// StatsText renders traffic figures.
func (p *PanelReporter) StatsText() string {
	if p.Health == nil {
		return "statistics are not available"
	}
	r := p.Health.Collect()
	var b strings.Builder
	b.WriteString("LAST HOUR\n")
	fmt.Fprintf(&b, "  requests   %d\n", r.Traffic.RequestsHour)
	fmt.Fprintf(&b, "  unique IPs %d\n", r.Traffic.UniqueIPsHour)
	fmt.Fprintf(&b, "  errors     %.1f%%\n", r.Traffic.ErrorRatePct)
	fmt.Fprintf(&b, "  active     %d connections\n\n", r.Traffic.ConnActive)
	fmt.Fprintf(&b, "nginx %s, %d workers\n",
		map[bool]string{true: "up", false: "DOWN"}[r.Nginx.Active], r.Nginx.Workers)
	if r.Security.AutoBanOn {
		fmt.Fprintf(&b, "auto-ban on, %d banned\n", r.Security.BansActive)
	}
	if r.Security.HoneypotOn {
		fmt.Fprintf(&b, "honeypot on (%s)\n", r.Security.HoneypotMode)
	}
	return b.String()
}

// ── formatting helpers ───────────────────────────────────────

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func hb(b int64) string {
	if b < 0 {
		b = 0
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	s := [...]string{"KB", "MB", "GB", "TB"}[exp]
	if v >= 100 {
		return fmt.Sprintf("%.0f%s", v, s)
	}
	return fmt.Sprintf("%.1f%s", v, s)
}

func shortDur(sec int64) string {
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}
