package web

// The server-health API.
//
// One endpoint, one struct. The panel page renders it, and the Telegram bot
// (next feature) will render the SAME struct — which is the whole reason
// internal/health exists as a package instead of as a handler here. Two
// surfaces deriving "the server is fine" independently would eventually
// disagree, and the operator would have no way to know which to trust.

import (
	"net/http"

	"shahrag/internal/certs"
	"shahrag/internal/config"
	"shahrag/internal/health"
	nginxpkg "shahrag/internal/nginx"
)

// initHealth builds the collector once, at startup.
//
// It has to be long-lived: every rate in the report (CPU, and above all the
// swap in/out rate) is the difference between the current sample and the
// previous one. A collector built per request would have no previous sample
// and could never report a rate at all — which is exactly the bug that makes
// most health pages show a swap GAUGE and call it a swap problem.
func (s *Server) initHealth() {
	s.healthC = health.NewCollector(health.Deps{
		Build: BuildTag,

		NginxActive:    nginxpkg.IsActive,
		NginxEnabled:   nginxpkg.IsEnabled,
		NginxVersion:   nginxpkg.Version,
		NginxWorkerCon: nginxpkg.WorkerConnections,
		NginxFailure:   nginxpkg.LastFailureReason,
		NginxConfTest: func() (bool, string) {
			if s.gen == nil {
				return true, ""
			}
			r := s.gen.Test()
			return r.OK, r.Stderr
		},

		HoneypotOn: func() (bool, string) {
			c, err := s.cfg.Read()
			if err != nil || c == nil {
				return false, ""
			}
			return c.Honeypot.Enabled, c.Honeypot.EffectiveMode()
		},
		AutoBanOn: func() (bool, string) {
			c, err := s.cfg.Read()
			if err != nil || c == nil {
				return false, ""
			}
			return c.AutoBan.Enabled && c.AutoBan.AnyRuleEnabled(), c.AutoBan.EffectiveAction()
		},
		BanCounts: func() (int, int, bool) {
			if s.bans == nil {
				return 0, 0, false
			}
			return len(s.bans.ActiveBans()), len(s.bans.Pending()), true
		},

		CertExpiry: func() health.Certs { return s.certHealth() },
		TrafficNow: func() health.Traffic {
			if s.stats == nil {
				return health.Traffic{}
			}
			sum := s.stats.Summary()
			return health.Traffic{
				RequestsHour:  sum.LastHour.Requests,
				ErrorRatePct:  sum.LastHour.ErrorRatePct,
				UniqueIPsHour: sum.LastHour.UniqueIPs,
				ConnActive:    sum.Connections.Active,
			}
		},
	})
}

// certHealth folds every configured domain's certificate into the summary
// the report wants.
//
// This reads and parses a PEM file per domain, which is why it is behind the
// probe cache in internal/health rather than being called on every report:
// with a dozen domains it is the second most expensive thing on the page
// after `nginx -t`.
func (s *Server) certHealth() health.Certs {
	c, err := s.cfg.Read()
	if err != nil || c == nil {
		return health.Certs{}
	}
	out := health.Certs{SoonestDays: 1 << 30}
	list := make([]certs.Info, 0, len(c.Domains))
	for name, d := range c.Domains {
		if d.Cert == "" && d.Key == "" {
			// A domain with no certificate configured is not a broken
			// certificate — it is a domain that has not been given one
			// yet, and reporting it as a problem would light the page up
			// red on a fresh install.
			continue
		}
		in := certs.Inspect(name, d.Cert, d.Key)
		list = append(list, in)
		if in.Error == "" && !in.Expired && in.DaysLeft < out.SoonestDays {
			out.SoonestDays = in.DaysLeft
			out.SoonestName = name
		}
	}
	sum := certs.Summarise(list)
	out.Total = sum.Total
	out.Expired = sum.Expired
	out.DueSoon = sum.DueSoon
	out.Problems = sum.Problems
	if out.SoonestDays == 1<<30 {
		out.SoonestDays = 0
	}
	return out
}

func (s *Server) handleHealthReport(w http.ResponseWriter, r *http.Request) {
	if s.healthC == nil {
		// Should not happen — NewServer wires it — but a nil here would
		// panic the whole panel rather than degrade one page.
		s.initHealth()
	}
	writeJSON(w, 200, s.healthC.Collect())
}

// invalidateHealthConfig is called after every regenerate+reload so the next
// report runs a fresh `nginx -t` instead of repeating the previous config's
// verdict.
func (s *Server) invalidateHealthConfig() {
	if s.healthC != nil {
		s.healthC.InvalidateConfig()
	}
}

var _ = config.BanActionNotFound
