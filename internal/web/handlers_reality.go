package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

type realityUpdateReq struct {
	Enabled   *bool    `json:"enabled"`
	HTTPPort  *int     `json:"http_port"`
	Resolvers []string `json:"resolvers"`
}

type realityServiceReq struct {
	Name      string `json:"name"`
	SNI       string `json:"sni"`
	LocalPort int    `json:"local_port"`
	Ports     []int  `json:"ports"`
	// Target: "" / localhost / 127.0.0.1 → local backend;
	// "$passthrough" → forward to the client's own SNI (unblock routing);
	// any hostname   → that host.
	Target string `json:"target"`
}

type realityServiceUpdateReq struct {
	SNI       *string `json:"sni"`
	LocalPort *int    `json:"local_port"`
	Target    *string `json:"target"`
	Ports     []int   `json:"ports"`
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
		if body.Resolvers != nil {
			clean := make([]string, 0, len(body.Resolvers))
			for _, rv := range body.Resolvers {
				if rv = strings.TrimSpace(rv); rv != "" {
					clean = append(clean, rv)
				}
			}
			// Syntax only here; whether the resolver actually loops is
			// decided by probing it below, which needs the network and so
			// must not happen inside the config mutation.
			if err := config.ValidateResolvers(clean); err != nil {
				return errValidation{err}
			}
			c.Reality.Resolvers = clean
		}
		return nil
	})
	if err != nil {
		// A rejected resolver is the operator's input, not a server fault,
		// so it must be a 400 — the UI shows 4xx inline on the form while a
		// 500 reads as "the panel is broken".
		var ve errValidation
		if errors.As(err, &ve) {
			writeErr(w, 400, ve.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}

	// Now that the value is stored, ASK each loopback resolver what it says
	// about a domain the panel relays. Answering with this server's own
	// address means it is the rewriting DNS and nginx would loop on it.
	// This is reported as a warning rather than a hard failure: the setting
	// is legitimate right up until a pass-through rule exists, and the
	// generator refuses the resolver anyway.
	warning := ""
	if c, rerr := s.cfg.Read(); rerr == nil {
		if relayed := c.FirstPassthroughDomain(); relayed != "" {
			selfIPs := append(nginxpkg.LocalIPv4(), "127.0.0.1")
			for _, rv := range c.Reality.Resolvers {
				// Probe every resolver: a rewriting AdGuard is just as likely
				// to sit on the LAN or on another host as on 127.0.0.1.
				if lerr := config.CheckResolverLoop(rv, relayed, selfIPs, 3*time.Second); lerr != nil {
					warning = lerr.Error()
					break
				}
			}
		}
	}
	resp := map[string]interface{}{"ok": true}
	if warning != "" {
		resp["warning"] = warning
	}
	writeJSON(w, 200, resp)
}

// errValidation marks an error caused by user input rather than by a failure
// inside the panel, so the handler can answer 400 instead of 500.
type errValidation struct{ err error }

func (e errValidation) Error() string { return e.err.Error() }
func (e errValidation) Unwrap() error { return e.err }

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
		target := strings.TrimSpace(body.Target)
		if config.IsLocalTarget(target) {
			target = ""
		}
		c.Reality.Services[body.Name] = config.RealityService{
			SNI:       body.SNI,
			LocalPort: body.LocalPort,
			Ports:     body.Ports,
			Target:    target,
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
		if body.Target != nil {
			t := strings.TrimSpace(*body.Target)
			if config.IsLocalTarget(t) {
				t = ""
			}
			svc.Target = t
		}
		if len(body.Ports) > 0 {
			svc.Ports = body.Ports
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
