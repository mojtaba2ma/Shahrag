package web

import (
	"net/http"

	"shahrag/internal/config"
)

type fakeSiteReq struct {
	Mode       string `json:"mode"`
	Content    string `json:"content"`
	SourcePath string `json:"source_path"`
}

func (s *Server) handleGetFakeSite(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c.FakeSite)
}

func (s *Server) handleSetFakeSite(w http.ResponseWriter, r *http.Request) {
	var body fakeSiteReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if body.Mode != "default" && body.Mode != "custom_content" && body.Mode != "custom_file" {
		writeErr(w, 400, "Invalid mode")
		return
	}
	if body.Mode == "custom_file" && body.SourcePath == "" {
		writeErr(w, 400, "source_path required")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		c.FakeSite = config.FakeSite{
			Mode:       body.Mode,
			Content:    body.Content,
			SourcePath: body.SourcePath,
		}
		if body.Mode != "custom_content" {
			c.FakeSite.Content = ""
		}
		if body.Mode != "custom_file" {
			c.FakeSite.SourcePath = ""
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
