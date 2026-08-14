package web

import (
	"net/http"

	"shahrag/internal/installer"
)

func (s *Server) handleInstallStatus(w http.ResponseWriter, r *http.Request) {
	installed := s.installer.IsInstalled()
	resp := map[string]interface{}{"installed": installed}
	if !installed {
		defs, err := s.installer.Defaults()
		if err == nil {
			resp["defaults"] = defs
		}
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleInstallRun(w http.ResponseWriter, r *http.Request) {
	var p installer.Params
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, "Invalid request: "+err.Error())
		return
	}
	if s.installer.IsInstalled() {
		writeErr(w, 400, "Already installed")
		return
	}
	result, err := s.installer.Install(p)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Refresh session secret (it was set during install)
	s.refreshSessionSecret()
	// Generate nginx config
	genResult, err := s.gen.GenerateAndReload()
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{
			"ok":    true,
			"panel": result,
			"nginx": map[string]string{"error": err.Error()},
		})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":    true,
		"panel": result,
		"nginx": genResult,
	})
}
