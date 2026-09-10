package web

// Trusted proxies and never-ban lists.

import (
	"net/http"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

type proxyListView struct {
	config.ProxyList
	Enabled bool `json:"enabled"`
	Count   int  `json:"count"`
	// Builtin distinguishes a shipped list from one the operator added,
	// so the UI can offer delete on one and not the other.
	Builtin bool `json:"builtin"`
}

func (s *Server) handleGetProxies(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	tp := c.TrustedProxies

	builtin := map[string]bool{}
	for _, l := range config.BuiltinProxyLists {
		builtin[l.ID] = true
	}

	out := make([]proxyListView, 0, len(tp.AllLists()))
	for _, l := range tp.AllLists() {
		out = append(out, proxyListView{
			ProxyList: l,
			Enabled:   tp.ListEnabled(l.ID),
			Count:     len(l.CIDRs),
			Builtin:   builtin[l.ID],
		})
	}

	writeJSON(w, 200, map[string]interface{}{
		"lists":                 out,
		"extra_trusted_cidrs":   tp.ExtraTrustedCIDRs,
		"extra_never_ban_cidrs": tp.ExtraNeverBanCIDRs,
		"trusted_count":         countRanges(tp.RealIPRanges()),
		"never_ban_count":       len(tp.NeverBanRanges()),
		"warnings":              config.TrustedProxyWarnings(tp, c),
		"summary":               nginxpkg.FrontProxySummary(c),
	})
}

func countRanges(m map[string][]string) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

type proxyReq struct {
	Enabled            map[string]bool `json:"enabled"`
	ExtraTrustedCIDRs  *[]string       `json:"extra_trusted_cidrs"`
	ExtraNeverBanCIDRs *[]string       `json:"extra_never_ban_cidrs"`
}

func (s *Server) handleSetProxies(w http.ResponseWriter, r *http.Request) {
	var body proxyReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		tp := c.TrustedProxies
		if body.Enabled != nil {
			if tp.Enabled == nil {
				tp.Enabled = map[string]bool{}
			}
			// Only known ids, so a stale client cannot accumulate
			// mystery entries in the config.
			known := map[string]bool{}
			for _, l := range tp.AllLists() {
				known[l.ID] = true
			}
			for id, on := range body.Enabled {
				if known[id] {
					tp.Enabled[id] = on
				}
			}
		}
		if body.ExtraTrustedCIDRs != nil {
			tp.ExtraTrustedCIDRs = trimList(*body.ExtraTrustedCIDRs)
		}
		if body.ExtraNeverBanCIDRs != nil {
			tp.ExtraNeverBanCIDRs = trimList(*body.ExtraNeverBanCIDRs)
		}
		if err := config.ValidateTrustedProxies(tp); err != nil {
			return err
		}
		c.TrustedProxies = tp
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c, _ := s.cfg.Read()
	out := applied(s.autoApply())
	out["warnings"] = config.TrustedProxyWarnings(c.TrustedProxies, c)
	out["trusted_count"] = countRanges(c.TrustedProxies.RealIPRanges())
	out["never_ban_count"] = len(c.TrustedProxies.NeverBanRanges())
	writeJSON(w, 200, out)
}
