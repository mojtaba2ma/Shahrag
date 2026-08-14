package web

import (
	"net/http"

	"shahrag/internal/config"
)

type domainReq struct {
	Name string `json:"name"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type domainUpdateReq struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c.Domains)
}

func (s *Server) handleCreateDomain(w http.ResponseWriter, r *http.Request) {
	var body domainReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if body.Name == "" {
		writeErr(w, 400, "Domain name required")
		return
	}
	if err := s.cfg.AddDomain(body.Name, body.Cert, body.Key); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "name": body.Name})
}

func (s *Server) handleGetDomain(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c, _ := s.cfg.Read()
	d, ok := c.Domains[name]
	if !ok {
		writeErr(w, 404, "Domain not found")
		return
	}
	type used struct {
		Service    string `json:"service"`
		Subdomain  string `json:"subdomain"`
		LocalPort  int    `json:"local_port"`
		ListenPort int    `json:"listen_port"`
		Path       string `json:"path"`
	}
	var usedBy []used
	for sname, svc := range c.Services {
		for _, b := range svc.Bindings {
			if b.Domain == name {
				usedBy = append(usedBy, used{
					Service: sname, Subdomain: b.Subdomain,
					LocalPort: svc.LocalPort, ListenPort: svc.ListenPort, Path: svc.Path,
				})
			}
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"name":     name,
		"cert":     d.Cert,
		"key":      d.Key,
		"services": usedBy,
	})
}

func (s *Server) handleUpdateDomain(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body domainUpdateReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		if _, ok := c.Domains[name]; !ok {
			return errNotFound
		}
		d := c.Domains[name]
		d.Cert = body.Cert
		d.Key = body.Key
		c.Domains[name] = d
		return nil
	})
	if err != nil {
		if err == errNotFound {
			writeErr(w, 404, "Domain not found")
		} else {
			writeErr(w, 500, err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteDomain(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.cfg.DeleteDomain(name); err != nil {
		if isNotExist(err) {
			writeErr(w, 404, "Domain not found")
		} else {
			writeErr(w, 400, err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
