package web

import (
	"net/http"
	"strconv"

	"shahrag/internal/config"
)

type realityUpdateReq struct {
	Enabled  *bool `json:"enabled"`
	HTTPPort *int  `json:"http_port"`
}

type realityServiceReq struct {
	Name      string `json:"name"`
	SNI       string `json:"sni"`
	LocalPort int    `json:"local_port"`
	Ports     []int  `json:"ports"`
}

type realityServiceUpdateReq struct {
	SNI       *string `json:"sni"`
	LocalPort *int    `json:"local_port"`
}

type realityPortReq struct {
	Port int `json:"port"`
}

func (s *Server) handleGetReality(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c.Reality)
}

func (s *Server) handleUpdateReality(w http.ResponseWriter, r *http.Request) {
	var body realityUpdateReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		if body.Enabled != nil {
			c.Reality.Enabled = *body.Enabled
		}
		if body.HTTPPort != nil {
			c.Reality.HTTPPort = *body.HTTPPort
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleCreateRealityService(w http.ResponseWriter, r *http.Request) {
	var body realityServiceReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		if _, ok := c.Reality.Services[body.Name]; ok {
			return errExists
		}
		if c.Reality.Services == nil {
			c.Reality.Services = map[string]config.RealityService{}
		}
		c.Reality.Services[body.Name] = config.RealityService{
			SNI:       body.SNI,
			LocalPort: body.LocalPort,
			Ports:     body.Ports,
		}
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "name": body.Name})
}

func (s *Server) handleUpdateRealityService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body realityServiceUpdateReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		svc, ok := c.Reality.Services[name]
		if !ok {
			return errNotFound
		}
		if body.SNI != nil {
			svc.SNI = *body.SNI
		}
		if body.LocalPort != nil {
			svc.LocalPort = *body.LocalPort
		}
		c.Reality.Services[name] = svc
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

func (s *Server) handleDeleteRealityService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		if _, ok := c.Reality.Services[name]; !ok {
			return errNotFound
		}
		delete(c.Reality.Services, name)
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

func (s *Server) handleAddRealityPort(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body realityPortReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		svc, ok := c.Reality.Services[name]
		if !ok {
			return errNotFound
		}
		for _, p := range svc.Ports {
			if p == body.Port {
				return errExists
			}
		}
		svc.Ports = append(svc.Ports, body.Port)
		c.Reality.Services[name] = svc
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveRealityPort(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil {
		writeErr(w, 400, "Invalid port")
		return
	}
	_, err = s.cfg.Mutate(func(c *config.Config) error {
		svc, ok := c.Reality.Services[name]
		if !ok {
			return errNotFound
		}
		out := svc.Ports[:0]
		found := false
		for _, p := range svc.Ports {
			if p == port {
				found = true
				continue
			}
			out = append(out, p)
		}
		if !found {
			return errNotFound
		}
		svc.Ports = out
		c.Reality.Services[name] = svc
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
