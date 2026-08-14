package web

import (
	"net/http"
	"os"
	"strings"

	"shahrag/internal/config"
	"shahrag/internal/installer"
)

func (s *Server) handleInstallStatus(w http.ResponseWriter, r *http.Request) {
	installed := s.installer.IsInstalled()
	resp := map[string]interface{}{
		"installed":      installed,
		"token_required": !installed && installer.TokenRequired(),
	}
	if !installed {
		defs, err := s.installer.Defaults()
		if err == nil {
			resp["defaults"] = defs
		}
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleInstallRun(w http.ResponseWriter, r *http.Request) {
	// Recovery path: the panel is already configured but its nginx config
	// was never generated successfully (e.g. a failed wizard run). Let an
	// authenticated admin regenerate instead of rejecting with
	// "Already installed", which used to leave servers stuck.
	if s.installer.IsInstalled() {
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			genResult, err := s.gen.GenerateAndReload()
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, 200, map[string]interface{}{"ok": true, "nginx": genResult})
		})(w, r)
		return
	}

	// First-run install: it is gated by the one-time token that install.sh
	// wrote to disk and printed to the admin. Without it, anyone who can
	// reach port 8080 could hijack the panel before the admin does.
	if err := installer.VerifyToken(strings.TrimSpace(r.Header.Get("X-Install-Token"))); err != nil {
		writeErr(w, 403, err.Error())
		return
	}

	var p installer.Params
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, "Invalid request: "+err.Error())
		return
	}

	// Snapshot the config file so a failure later in this handler can put
	// the panel back into its pre-install state (the wizard stays retryable).
	var configBackup []byte
	if data, err := os.ReadFile(config.ConfigPath); err == nil {
		configBackup = data
	}

	result, err := s.installer.Install(p)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	// Refresh session secret (it was set during install)
	s.refreshSessionSecret()

	// Generate nginx config. On failure, roll the config back so the panel
	// remains in a clean, retryable state.
	genResult, err := s.gen.GenerateAndReload()
	if err != nil {
		_ = restoreConfigBytes(configBackup)
		writeErr(w, 500, "install wrote config but nginx generation failed: "+err.Error())
		return
	}
	if ok, _ := genResult["ok"].(bool); !ok {
		_ = restoreConfigBytes(configBackup)
		writeErr(w, 500, "install completed but nginx rejected the generated config — rolled back. Fix the underlying nginx issue and retry.")
		return
	}

	// Success: consume the one-time token and, if the panel port changed,
	// restart the service shortly (after this response is sent) so it binds
	// the configured port.
	installer.ConsumeToken()
	s.scheduleRestartIfPortChanged(p.LocalPort)

	writeJSON(w, 200, map[string]interface{}{
		"ok":    true,
		"panel": result,
		"nginx": genResult,
	})
}

// restoreConfigBytes restores a previously captured config file. Used to roll
// the panel back when the post-install nginx generation fails.
func restoreConfigBytes(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return os.WriteFile(config.ConfigPath, data, 0o600)
}
