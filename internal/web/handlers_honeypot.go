package web

// The honeypot's API.
//
// One GET to read the current state and everything needed to render the
// form, one PUT to change it, and one endpoint to read back who has tripped
// it. Saving applies immediately, like every other change since r38.

import (
	"bufio"
	"net/http"
	"os"
	"sort"
	"strings"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

type honeypotResp struct {
	config.Honeypot
	// Enabled is restated without omitempty. The embedded struct omits it
	// when false, so the API answered a disabled trap by leaving the field
	// out entirely — which works in JavaScript by accident (undefined is
	// falsy) and would break the first client that checked for the key.
	// An API should say "false", not stay silent.
	Enabled bool `json:"enabled"`
	// LogHits, restated for exactly the same reason as Enabled above: the
	// embedded struct tags it omitempty, so a deliberate "false" vanished
	// from the response entirely. A client reading the key then saw
	// `undefined`, which is falsy in JavaScript and therefore worked by
	// accident — until anything checked whether the key was PRESENT. An
	// API should say false, not stay silent.
	LogHits bool `json:"log_hits"`
	// DefaultPaths lets the UI show what the trap covers without the
	// operator having to type the list or the panel having to duplicate it
	// in JavaScript — one source of truth, in Go.
	DefaultPaths []string `json:"default_paths"`
	// EffectivePaths is what will actually be generated, after the extras
	// and the exceptions are applied. Seeing the real list is the only way
	// to be sure an exception took effect.
	EffectivePaths []string `json:"effective_paths"`
	// Conflicts names trap paths that a real service also serves. Shown as
	// a warning rather than an error, because the operator may have just
	// added the exception and not saved yet.
	Conflicts []string `json:"conflicts,omitempty"`
	// LogPath is where hits are recorded.
	LogPath string `json:"log_path"`
	// Defaults, so the form can show them as placeholders.
	DefaultRate int `json:"default_rate_per_minute"`
}

func (s *Server) handleGetHoneypot(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	h := c.Honeypot
	// Normalise on the way out so the UI never has to guess what an empty
	// or unknown mode means.
	h.Mode = h.EffectiveMode()
	// First time this form is opened, recommend recording the hits.
	//
	// Switching a detector on and not keeping what it detects is not a
	// useful state, and nobody thinks to tick a second box. But once the
	// form has been SAVED, whatever was chosen stands for ever — including
	// "off". Configured is what tells those two situations apart; without
	// it, a stored `false` is indistinguishable from a field that has
	// never been set, and the panel would keep switching logging back on
	// under an operator who deliberately turned it off.
	if !h.Configured {
		h.LogHits = true
	}
	writeJSON(w, 200, honeypotResp{
		Honeypot:       h,
		Enabled:        h.Enabled,
		LogHits:        h.LogHits,
		DefaultPaths:   config.DefaultHoneypotPaths,
		EffectivePaths: c.Honeypot.EffectivePaths(),
		Conflicts:      config.HoneypotConflicts(c),
		LogPath:        nginxpkg.HoneypotLogPath,
		DefaultRate:    config.DefaultHoneypotRate,
	})
}

type honeypotReq struct {
	Enabled       *bool     `json:"enabled"`
	Mode          *string   `json:"mode"`
	ExtraPaths    *[]string `json:"extra_paths"`
	AllowPaths    *[]string `json:"allow_paths"`
	AllowIPs      *[]string `json:"allow_ips"`
	RatePerMinute *int      `json:"rate_per_minute"`
	LogHits       *bool     `json:"log_hits"`
}

func (s *Server) handleSetHoneypot(w http.ResponseWriter, r *http.Request) {
	var body honeypotReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}

	var conflicts []string
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		h := c.Honeypot
		if body.Enabled != nil {
			h.Enabled = *body.Enabled
		}
		if body.Mode != nil {
			h.Mode = config.NormalizeHoneypotMode(*body.Mode)
		}
		if body.ExtraPaths != nil {
			h.ExtraPaths = trimList(*body.ExtraPaths)
		}
		if body.AllowPaths != nil {
			h.AllowPaths = trimList(*body.AllowPaths)
		}
		if body.AllowIPs != nil {
			h.AllowIPs = trimList(*body.AllowIPs)
		}
		if body.RatePerMinute != nil {
			h.RatePerMinute = *body.RatePerMinute
		}
		if body.LogHits != nil {
			h.LogHits = *body.LogHits
		}
		// Saving the form is what makes the choice explicit from now on.
		h.Configured = true
		if err := config.ValidateHoneypot(h); err != nil {
			return err
		}
		c.Honeypot = h
		// A trap that shadows a working service would break the site in a
		// way that is very hard to diagnose: the location looks correct in
		// the panel and the traffic simply never arrives. Reported back so
		// the UI can warn, not refused — the operator may have deliberately
		// baited a path they no longer serve.
		conflicts = config.HoneypotConflicts(c)
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	out := applied(s.autoApply())
	if len(conflicts) > 0 {
		out["conflicts"] = conflicts
	}
	c, _ := s.cfg.Read()
	out["effective_paths"] = c.Honeypot.EffectivePaths()
	writeJSON(w, 200, out)
}

// honeypotHit is one recorded trip.
type honeypotHit struct {
	Time string `json:"time"`
	IP   string `json:"ip"`
	Req  string `json:"request"`
	UA   string `json:"ua"`
}

// handleHoneypotHits returns the most recent trips, newest first.
//
// Read from the log nginx writes rather than kept in the panel's memory:
// the panel restarts, nginx keeps logging, and an operator investigating an
// incident wants the whole file, not whatever survived the last upgrade.
func (s *Server) handleHoneypotHits(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	if limit > 1000 {
		limit = 1000
	}

	f, err := os.Open(nginxpkg.HoneypotLogPath)
	if err != nil {
		// A missing file is the normal state: either logging is off or
		// nothing has tripped the trap yet. Not an error.
		writeJSON(w, 200, map[string]interface{}{
			"hits": []honeypotHit{}, "total": 0, "path": nginxpkg.HoneypotLogPath,
		})
		return
	}
	defer f.Close()

	var all []honeypotHit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if h, ok := parseHoneypotLine(sc.Text()); ok {
			all = append(all, h)
		}
	}

	// Newest first, then truncate: an operator looks at the most recent
	// activity, and a log that has been running for months could be large.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	total := len(all)
	if len(all) > limit {
		all = all[:limit]
	}
	if all == nil {
		all = []honeypotHit{}
	}

	// A per-address tally answers the question actually being asked:
	// "who is hammering me?"
	counts := map[string]int{}
	for _, h := range all {
		counts[h.IP]++
	}
	type ipCount struct {
		IP    string `json:"ip"`
		Count int    `json:"count"`
	}
	tops := make([]ipCount, 0, len(counts))
	for ip, n := range counts {
		tops = append(tops, ipCount{ip, n})
	}
	sort.Slice(tops, func(i, j int) bool {
		if tops[i].Count != tops[j].Count {
			return tops[i].Count > tops[j].Count
		}
		return tops[i].IP < tops[j].IP
	})
	if len(tops) > 10 {
		tops = tops[:10]
	}

	writeJSON(w, 200, map[string]interface{}{
		"hits": all, "total": total, "top": tops,
		"path": nginxpkg.HoneypotLogPath,
	})
}

// parseHoneypotLine reads one line of the shg_honeypot log format:
//
//	2026-09-07T12:00:00+00:00 1.2.3.4 "GET /.env HTTP/1.1" ua="curl" ref="-"
func parseHoneypotLine(line string) (honeypotHit, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return honeypotHit{}, false
	}
	var h honeypotHit
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 {
		return honeypotHit{}, false
	}
	h.Time, h.IP = parts[0], parts[1]
	rest := parts[2]

	if i := strings.Index(rest, "\""); i >= 0 {
		if j := strings.Index(rest[i+1:], "\""); j >= 0 {
			h.Req = rest[i+1 : i+1+j]
			rest = rest[i+2+j:]
		}
	}
	if i := strings.Index(rest, "ua=\""); i >= 0 {
		if j := strings.Index(rest[i+4:], "\""); j >= 0 {
			h.UA = rest[i+4 : i+4+j]
		}
	}
	if h.Req == "" {
		return honeypotHit{}, false
	}
	return h, true
}

// trimList cleans a list of operator-typed strings.
func trimList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
