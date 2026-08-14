package web

import (
	"net/http"
	"strconv"
)

type portReq struct {
	Port int `json:"port"`
}

func (s *Server) handleListPorts(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	type entry struct {
		Port   int      `json:"port"`
		UsedBy []string `json:"used_by"`
		IsHTTP bool     `json:"is_http"`
	}
	var out []entry
	for _, p := range c.ListenPorts {
		var used []string
		for name, svc := range c.Services {
			if svc.ListenPort == p {
				used = append(used, name)
			}
		}
		out = append(out, entry{Port: p, UsedBy: used, IsHTTP: p == 80})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleAddPort(w http.ResponseWriter, r *http.Request) {
	var body portReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if err := s.cfg.AddPort(body.Port); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "port": body.Port})
}

func (s *Server) handleDeletePort(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil {
		writeErr(w, 400, "Invalid port")
		return
	}
	if err := s.cfg.DeletePort(port); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
