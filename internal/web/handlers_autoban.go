package web

// The automatic-ban API.

import (
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"shahrag/internal/banner"
	"shahrag/internal/config"
)

type autoBanResp struct {
	config.AutoBan
	// Restated without omitempty: the embedded struct hides a false, and
	// an API should say "false" rather than stay silent.
	Enabled bool `json:"enabled"`
	// LogBans, restated without omitempty for the same reason: a stored
	// false was omitted from the response, so the checkbox could never
	// render as deliberately unticked.
	LogBans bool `json:"log_bans"`
	// Defaults so the form can show them without duplicating them in JS.
	Defaults config.AutoBan `json:"defaults"`
	// Live counters.
	BanCount     int `json:"ban_count"`
	PendingCount int `json:"pending_count"`
	// Running says whether the watcher is actually going. If it is not,
	// the settings are stored but nothing will ever be banned, and the UI
	// must be able to say so rather than implying protection.
	Running bool `json:"running"`
}

func (s *Server) handleGetAutoBan(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	ab := c.AutoBan
	ab.Action = ab.EffectiveAction()
	// A config that predates this feature has zeroed rules, which would
	// render as an empty form. Fill it from the defaults so the page shows
	// sensible starting numbers.
	if !ab.AnyRuleEnabled() && !ab.Enabled {
		d := config.DefaultAutoBan()
		d.Enabled = false
		d.Configured = ab.Configured
		d.LogBans = ab.LogBans
		ab = d
	}
	// Same reasoning as the honeypot: recommend keeping a record the first
	// time, then never override the operator's saved choice. The active
	// ban table only shows bans that have not yet expired, so without this
	// file "was this visitor blocked last night?" is unanswerable.
	// One source of truth, shared with the engine.
	ab.LogBans = ab.ShouldLogBans()
	resp := autoBanResp{
		AutoBan:  ab,
		Enabled:  c.AutoBan.Enabled,
		LogBans:  ab.LogBans,
		Defaults: config.DefaultAutoBan(),
		Running:  s.bans != nil,
	}
	if s.bans != nil {
		resp.BanCount = len(s.bans.ActiveBans())
		resp.PendingCount = len(s.bans.Pending())
	}
	writeJSON(w, 200, resp)
}

type autoBanReq struct {
	Enabled      *bool               `json:"enabled"`
	Action       *string             `json:"action"`
	Honeypot     *config.AutoBanRule `json:"honeypot"`
	AuthFail     *config.AutoBanRule `json:"auth_fail"`
	NotFound     *config.AutoBanRule `json:"not_found"`
	ErrorRate    *config.AutoBanRule `json:"error_rate"`
	AllowIPs     *[]string           `json:"allow_ips"`
	ThrottleRate *int                `json:"throttle_rate"`
	MaxBans      *int                `json:"max_bans"`
	LogBans      *bool               `json:"log_bans"`
}

func (s *Server) handleSetAutoBan(w http.ResponseWriter, r *http.Request) {
	var body autoBanReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		ab := c.AutoBan
		// Starting from the defaults when nothing has been configured
		// means a partial request cannot produce a half-empty rule set.
		if !ab.AnyRuleEnabled() {
			ab = config.DefaultAutoBan()
		}
		if body.Enabled != nil {
			ab.Enabled = *body.Enabled
		}
		if body.Action != nil {
			ab.Action = config.NormalizeBanAction(*body.Action)
		}
		if body.Honeypot != nil {
			ab.Honeypot = *body.Honeypot
		}
		if body.AuthFail != nil {
			ab.AuthFail = *body.AuthFail
		}
		if body.NotFound != nil {
			ab.NotFound = *body.NotFound
		}
		if body.ErrorRate != nil {
			ab.ErrorRate = *body.ErrorRate
		}
		if body.AllowIPs != nil {
			cleaned, err := validateIPList(*body.AllowIPs)
			if err != nil {
				return err
			}
			ab.AllowIPs = cleaned
		}
		if body.ThrottleRate != nil {
			ab.ThrottleRate = *body.ThrottleRate
		}
		if body.MaxBans != nil {
			ab.MaxBans = *body.MaxBans
		}
		if body.LogBans != nil {
			ab.LogBans = *body.LogBans
		}
		ab.Configured = true
		if err := config.ValidateAutoBan(ab); err != nil {
			return err
		}
		c.AutoBan = ab
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, applied(s.autoApply()))
}

// validateIPList rejects entries the generator would silently drop. Silent
// dropping is wrong here: an operator who mistypes their own address and
// sees no error believes they are exempt when they are not.
func validateIPList(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(v); err != nil && net.ParseIP(v) == nil {
			return nil, &ipError{v}
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out, nil
}

type ipError struct{ v string }

func (e *ipError) Error() string {
	return e.v + " is not a valid IP address or CIDR range"
}

// handleBanLog returns the durable record of bans and releases.
//
// Separate from /api/autoban/bans, which lists only what is banned RIGHT
// NOW. The two answer different questions and conflating them is what makes
// an expired ban impossible to investigate.
func (s *Server) handleBanLog(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 200)
	if limit > 2000 {
		limit = 2000
	}
	lines := banner.ReadBanLog(limit)
	out := make([]map[string]interface{}, 0, len(lines))
	for _, ln := range lines {
		if ev, ok := parseBanLogLine(ln); ok {
			out = append(out, ev)
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"events": out, "path": banner.BanLogPath, "total": len(out),
	})
}

// parseBanLogLine reads one line of the ban log:
//
//	2026-09-08T14:03:11+03:30 203.0.113.5 "ban honeypot" hits="3" until="..."
func parseBanLogLine(line string) (map[string]interface{}, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 {
		return nil, false
	}
	out := map[string]interface{}{"time": parts[0], "ip": parts[1]}
	rest := parts[2]
	field := func(prefix string) string {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return ""
		}
		j := strings.Index(rest[i+len(prefix):], `"`)
		if j < 0 {
			return ""
		}
		return rest[i+len(prefix) : i+len(prefix)+j]
	}
	// The action+reason is the first quoted run.
	if i := strings.Index(rest, `"`); i >= 0 {
		if j := strings.Index(rest[i+1:], `"`); j >= 0 {
			desc := rest[i+1 : i+1+j]
			bits := strings.SplitN(desc, " ", 2)
			out["action"] = bits[0]
			if len(bits) > 1 {
				out["reason"] = bits[1]
			}
		}
	}
	out["hits"] = field(`hits="`)
	out["until"] = field(`until="`)
	if out["action"] == nil {
		return nil, false
	}
	return out, true
}

type banView struct {
	IP        string `json:"ip"`
	Reason    string `json:"reason"`
	Hits      int    `json:"hits"`
	BannedAt  string `json:"banned_at"`
	ExpiresAt string `json:"expires_at"`
	Permanent bool   `json:"permanent"`
	// RemainingMinutes is what the operator actually wants to know.
	RemainingMinutes int `json:"remaining_minutes"`
}

func (s *Server) handleListBans(w http.ResponseWriter, r *http.Request) {
	if s.bans == nil {
		writeJSON(w, 200, map[string]interface{}{
			"bans": []banView{}, "running": false, "pending": map[string]int{},
		})
		return
	}
	now := time.Now()
	list := s.bans.ActiveBans()
	out := make([]banView, 0, len(list))
	for _, b := range list {
		out = append(out, banView{
			IP: b.IP, Reason: b.Reason, Hits: b.Hits,
			BannedAt:         b.BannedAt.Format(time.RFC3339),
			ExpiresAt:        b.ExpiresAt.Format(time.RFC3339),
			Permanent:        b.Permanent,
			RemainingMinutes: int(b.Remaining(now).Minutes()),
		})
	}
	writeJSON(w, 200, map[string]interface{}{
		"bans": out, "running": true, "pending": s.bans.Pending(),
	})
}

func (s *Server) handleAddBan(w http.ResponseWriter, r *http.Request) {
	if s.bans == nil {
		writeErr(w, 503, "the ban engine is not running")
		return
	}
	var body struct {
		IP        string `json:"ip"`
		Minutes   int    `json:"minutes"`
		Permanent bool   `json:"permanent"`
		Reason    string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	d := time.Duration(body.Minutes) * time.Minute
	if body.Minutes <= 0 && !body.Permanent {
		d = config.DefaultBanMinutes * time.Minute
	}
	if err := s.bans.Ban(body.IP, body.Reason, d, body.Permanent); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, applied(s.autoApply()))
}

func (s *Server) handleUnban(w http.ResponseWriter, r *http.Request) {
	if s.bans == nil {
		writeErr(w, 503, "the ban engine is not running")
		return
	}
	ip := r.PathValue("ip")
	if !s.bans.Unban(ip) {
		writeErr(w, 404, "that address is not banned")
		return
	}
	writeJSON(w, 200, applied(s.autoApply()))
}

func (s *Server) handleUnbanAll(w http.ResponseWriter, r *http.Request) {
	if s.bans == nil {
		writeErr(w, 503, "the ban engine is not running")
		return
	}
	n := s.bans.UnbanAll()
	out := applied(s.autoApply())
	out["removed"] = n
	writeJSON(w, 200, out)
}
