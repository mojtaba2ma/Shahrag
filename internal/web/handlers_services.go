package web

import (
	"net/http"
	"strconv"
	"strings"

	"shahrag/internal/config"
)

type serviceReq struct {
	Name        string `json:"name"`
	Subdomain   string `json:"subdomain"`
	Domain      string `json:"domain"`
	LocalPort   int    `json:"local_port"`
	ListenPort  int    `json:"listen_port"`
	Path        string `json:"path"`
	PathOwned   bool   `json:"path_owned"`
	SSLBackend  bool   `json:"ssl_backend"`
}

type serviceUpdateReq struct {
	LocalPort   *int   `json:"local_port"`
	ListenPort  *int   `json:"listen_port"`
	Path        *string `json:"path"`
	PathOwned   *bool  `json:"path_owned"`
	SSLBackend  *bool  `json:"ssl_backend"`
}

type bindingReq struct {
	Subdomain string `json:"subdomain"`
	Domain    string `json:"domain"`
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c.Services)
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var body serviceReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || body.Subdomain == "" || body.Domain == "" {
		writeErr(w, 400, "name, subdomain and domain are required")
		return
	}
	path := strings.Trim(body.Path, "/")
	if path == "" {
		path = "/"
	}
	if err := s.cfg.AddService(body.Name, body.Subdomain, body.Domain,
		body.LocalPort, body.ListenPort, path, body.PathOwned, body.SSLBackend); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "name": body.Name})
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c, _ := s.cfg.Read()
	svc, ok := c.Services[name]
	if !ok {
		writeErr(w, 404, "Service not found")
		return
	}
	writeJSON(w, 200, svc)
}

func (s *Server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body serviceUpdateReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		svc, ok := c.Services[name]
		if !ok {
			return errNotFound
		}
		if body.LocalPort != nil {
			svc.LocalPort = *body.LocalPort
		}
		if body.ListenPort != nil {
			svc.ListenPort = *body.ListenPort
			if !intInSlice(c.ListenPorts, *body.ListenPort) {
				c.ListenPorts = append(c.ListenPorts, *body.ListenPort)
			}
		}
		if body.Path != nil {
			p := strings.Trim(*body.Path, "/")
			if p == "" {
				p = "/"
			}
			svc.Path = p
		}
		if body.PathOwned != nil {
			svc.PathOwned = *body.PathOwned
		}
		if body.SSLBackend != nil {
			svc.SSLBackend = *body.SSLBackend
		}
		c.Services[name] = svc
		return nil
	})
	if err != nil {
		if err == errNotFound {
			writeErr(w, 404, "Service not found")
		} else {
			writeErr(w, 400, err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.cfg.DeleteService(name); err != nil {
		if isNotExist(err) {
			writeErr(w, 404, "Service not found")
		} else {
			writeErr(w, 400, err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleAddBinding(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body bindingReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if err := s.cfg.AddBinding(name, body.Subdomain, body.Domain); err != nil {
		if isNotExist(err) {
			writeErr(w, 404, "Service not found")
		} else {
			writeErr(w, 400, err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveBinding(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	idxStr := r.PathValue("index")
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		writeErr(w, 400, "Invalid index")
		return
	}
	if err := s.cfg.RemoveBinding(name, idx); err != nil {
		if isNotExist(err) {
			writeErr(w, 404, "Binding not found")
		} else {
			writeErr(w, 400, err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func intInSlice(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
