package web

import (
	"strings"

	nginxpkg "shahrag/internal/nginx"
)

// Auto-apply.
//
// Every change that belongs in the nginx config used to need a second,
// separate click on "Generate and reload". Saving a service therefore left
// the panel and the running server disagreeing until someone remembered the
// second step — and a service that looked configured was not actually
// serving.
//
// Saving now regenerates and reloads by itself. This is safe because
// GenerateAndReload already:
//
//   - snapshots every file it is about to touch,
//   - runs `nginx -t` and restores the snapshot if the test fails,
//   - reloads (never restarts) and, if the reload fails, restores the
//     snapshot and reloads again so disk and the running process agree.
//
// So the worst case is "the change is rejected and nothing happened", never
// "nginx is down". The manual button stays, because it is also the way to
// re-apply after editing something outside the panel.
//
// The result is attached to the API response rather than replacing it: the
// save itself already succeeded, and a config that nginx rejects must be
// reported as a warning on a successful save, not as a failed save. Losing
// the saved data because the reload failed would be far worse.

// applyResult is the shape merged into a handler's JSON response.
type applyResult struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Restored bool   `json:"restored,omitempty"`
}

// autoApply regenerates the nginx configuration and reloads it.
//
// It never returns an error: the caller's write has already been committed,
// and the outcome here is advisory. A nil generator (tests) is a no-op.
func (s *Server) autoApply() applyResult {
	if s.gen == nil {
		return applyResult{OK: true}
	}
	// The health page caches the last `nginx -t` for a minute because the
	// probe forks. We have just replaced the very files it tested, so that
	// cached verdict now describes a config that no longer exists — drop
	// it, whatever the outcome below.
	defer s.invalidateHealthConfig()
	res, err := s.gen.GenerateAndReload()
	if err != nil {
		return applyResult{OK: false, Error: err.Error()}
	}
	out := applyResult{OK: true}
	if v, ok := res["ok"].(bool); ok {
		out.OK = v
	}
	if v, ok := res["restored"].(bool); ok && v {
		out.Restored = true
	}
	// Surface the reason nginx refused, so the toast can say more than
	// "failed". nginx puts the useful half — the file and line it objected
	// to — on stderr.
	if !out.OK {
		if t, ok := res["test"].(nginxpkg.TestResult); ok && !t.OK {
			out.Error = firstNonEmpty(t.Stderr, t.Stdout)
		}
		if r, ok := res["reload"].(nginxpkg.TestResult); ok && out.Error == "" && !r.OK {
			out.Error = firstNonEmpty(r.Stderr, r.Stdout)
		}
		if out.Error == "" {
			out.Error = "nginx rejected the generated configuration"
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// applied merges the outcome of the automatic regenerate+reload into a
// handler response. The save itself is reported as ok; the reload is a
// separate, advisory field.
func applied(res applyResult) map[string]interface{} {
	out := map[string]interface{}{"ok": true, "applied": res.OK}
	if res.Error != "" {
		out["apply_error"] = res.Error
	}
	if res.Restored {
		out["apply_restored"] = true
	}
	return out
}
