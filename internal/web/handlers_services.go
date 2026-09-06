package web

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

type serviceReq struct {
	Name       string `json:"name"`
	Subdomain  string `json:"subdomain"`
	Domain     string `json:"domain"`
	LocalPort  int    `json:"local_port"`
	ListenPort int    `json:"listen_port"`
	Path       string `json:"path"`
	PathOwned  bool   `json:"path_owned"`
	SSLBackend bool   `json:"ssl_backend"`
	Target     string `json:"target"`
	Gate       string `json:"gate"`
	GateSecret string `json:"gate_secret"`

	GateAllowPaths []string `json:"gate_allow_paths"`
	GateAllowIPs   []string `json:"gate_allow_ips"`
	GateAllowBots  bool     `json:"gate_allow_bots"`
}

type serviceUpdateReq struct {
	LocalPort  *int    `json:"local_port"`
	ListenPort *int    `json:"listen_port"`
	Path       *string `json:"path"`
	PathOwned  *bool   `json:"path_owned"`
	SSLBackend *bool   `json:"ssl_backend"`
	Target     *string `json:"target"`
	Gate       *string `json:"gate"`
	GateSecret *string `json:"gate_secret"`

	GateAllowPaths *[]string `json:"gate_allow_paths"`
	GateAllowIPs   *[]string `json:"gate_allow_ips"`
	GateAllowBots  *bool     `json:"gate_allow_bots"`

	// Disabled takes the service out of the generated config without
	// deleting it. A pointer so an update that omits it leaves the
	// current state alone.
	Disabled *bool `json:"disabled"`
}

type bindingReq struct {
	Subdomain string `json:"subdomain"`
	Domain    string `json:"domain"`
}

// applyGate validates a gate request and writes it onto the service.
//
// Shared by create and update so the two paths cannot drift. The rules:
//
//   - an unknown mode is refused rather than silently ignored, otherwise the
//     operator ticks a box, sees no error, and believes a service is
//     protected when it is not;
//   - "secret" mode needs a usable word. The alphabet is restricted because
//     the value travels as a raw cookie and is embedded in the generated
//     nginx config;
//   - "js" mode generates its own random token. It must be REGENERATED only
//     when the mode is being turned on, so that saving an unrelated field
//     later does not silently log every visitor out of the gate.
func applyGate(svc *config.Service, mode string, secret *string) error {
	want := config.NormalizeGate(mode)
	// NormalizeGate maps anything it does not recognise to "off". That is the
	// right default for reading a config file, but for an API request it
	// would turn a typo into a silently unprotected service, so reject it.
	if want == config.GateOff {
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "", "off", "none", "false", "0":
			// an explicit disable
		default:
			return fmt.Errorf("unknown protection mode %q", mode)
		}
	}

	switch want {
	case config.GateOff:
		// Clear the exceptions too: leaving a stale allow-list on a
		// service whose shield is off is confusing, and it would silently
		// come back if the shield were re-enabled later.
		svc.Gate = config.GateOff
		svc.GateSecret = ""
		svc.GateAllowPaths = nil
		svc.GateAllowIPs = nil
		svc.GateAllowBots = false
		return nil

	case config.GateSecret:
		val := ""
		if secret != nil {
			val = strings.TrimSpace(*secret)
		}
		// Keep the existing word when the caller sends none (editing an
		// unrelated field must not wipe the key).
		if val == "" && config.NormalizeGate(svc.Gate) == config.GateSecret {
			val = svc.GateSecret
		}
		if !nginxpkg.ValidGateSecret(val) {
			return fmt.Errorf("the access key must be 4-64 characters, letters, digits, - or _")
		}
		svc.Gate = config.GateSecret
		svc.GateSecret = val
		return nil

	default: // config.GateJS
		// Only mint a new token when switching INTO js mode, so an edit of
		// some other field does not invalidate everyone's cookie.
		if config.NormalizeGate(svc.Gate) != config.GateJS || svc.GateSecret == "" {
			tok, err := nginxpkg.NewGateToken()
			if err != nil {
				return fmt.Errorf("could not generate a token: %w", err)
			}
			svc.GateSecret = tok
		}
		svc.Gate = config.GateJS
		return nil
	}
}

// validateGateExceptions rejects entries the generator would silently drop.
//
// Silent dropping is the wrong behaviour here: an operator who types a bad
// CIDR and sees no error will believe their database server is exempt when
// it is not, and will only find out when the link breaks.
func validateGateExceptions(svc config.Service) error {
	if !svc.GateEnabled() {
		return nil
	}
	for _, p := range svc.GateAllowPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, "$'\" \t{};") {
			return fmt.Errorf("allowed path %q contains a character that is not permitted", p)
		}
	}
	for _, ip := range svc.GateAllowIPs {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(ip); err == nil {
			continue
		}
		if net.ParseIP(ip) != nil {
			continue
		}
		return fmt.Errorf("%q is not a valid IP address or CIDR range", ip)
	}
	return nil
}

// handleGenerateGateSecret returns a fresh, strong access key.
//
// Generated on the SERVER, not in the browser: the panel must work on
// plain HTTP over a LAN address, where window.crypto is available but the
// page itself is not a secure context, and more importantly the key must
// come from the same generator the validator trusts. Nothing is stored —
// the caller decides whether to save it.
func (s *Server) handleGenerateGateSecret(w http.ResponseWriter, r *http.Request) {
	key, err := nginxpkg.NewGateSecret()
	if err != nil {
		writeErr(w, 500, "could not generate a key: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"secret": key,
		"length": len(key),
	})
}

// handleToggleService switches a service on or off without deleting it.
//
// A dedicated endpoint rather than a general update: the list's toggle must
// not accidentally rewrite any other field, and this way the intent is
// explicit in the audit trail.
func (s *Server) handleToggleService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil || body.Enabled == nil {
		writeErr(w, 400, "enabled is required")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		svc, ok := c.Services[name]
		if !ok {
			return errNotFound
		}
		svc.Disabled = !*body.Enabled
		// Switching a service back ON can resurrect a conflict that was
		// dormant while it was off — for example if another service took
		// over its path in the meantime. Better to refuse the switch with a
		// clear reason than to produce a config nginx will not load.
		if err := config.CheckPathConflict(c, name, svc); err != nil {
			return err
		}
		c.Services[name] = svc
		return nil
	})
	if err != nil {
		if err == errNotFound {
			writeErr(w, 404, "Service not found")
		} else {
			writeServiceErr(w, 400, err)
		}
		return
	}
	out := applied(s.autoApply())
	out["enabled"] = *body.Enabled
	writeJSON(w, 200, out)
}

// handleToggleRealityService is the same switch for an SNI rule.
func (s *Server) handleToggleRealityService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil || body.Enabled == nil {
		writeErr(w, 400, "enabled is required")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		svc, ok := c.Reality.Services[name]
		if !ok {
			return errNotFound
		}
		svc.Disabled = !*body.Enabled
		c.Reality.Services[name] = svc
		return nil
	})
	if err != nil {
		if err == errNotFound {
			writeErr(w, 404, "Rule not found")
		} else {
			writeErr(w, 400, err.Error())
		}
		return
	}
	out := applied(s.autoApply())
	out["enabled"] = *body.Enabled
	writeJSON(w, 200, out)
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c.Services)
}

// writeServiceErr turns a config error into an API error, translating a
// duplicate-location clash into a code the browser can render in the
// operator's own language.
func writeServiceErr(w http.ResponseWriter, status int, err error) {
	var pc config.PathConflict
	if errors.As(err, &pc) {
		path := pc.Path
		if path == "" {
			path = "/"
		} else {
			path = "/" + path
		}
		writeTypedErr(w, status, "path_conflict", err.Error(), map[string]string{
			"existing": pc.Existing,
			"incoming": pc.Incoming,
			"host":     pc.Host,
			"path":     path,
			"port":     strconv.Itoa(pc.Port),
		})
		return
	}
	writeErr(w, status, err.Error())
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
	// Only the LEADING slash is removed: the generator adds it back, while a
	// TRAILING slash is the operator's choice and must survive ("/path/" is
	// a different, valid nginx location from "/path").
	path := config.NormalizePath(body.Path)
	if path == "" {
		path = "/"
	}
	// Validate the gate BEFORE creating anything, so a bad access key cannot
	// leave a half-configured service behind.
	var probe config.Service
	probe.GateAllowPaths = body.GateAllowPaths
	probe.GateAllowIPs = body.GateAllowIPs
	probe.GateAllowBots = body.GateAllowBots
	if err := applyGate(&probe, body.Gate, &body.GateSecret); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateGateExceptions(probe); err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	if err := s.cfg.AddServiceTarget(body.Name, body.Subdomain, body.Domain,
		body.LocalPort, body.ListenPort, path, body.PathOwned, body.SSLBackend,
		body.Target); err != nil {
		writeServiceErr(w, 400, err)
		return
	}

	if probe.Gate != config.GateOff {
		if _, err := s.cfg.Mutate(func(c *config.Config) error {
			svc, ok := c.Services[body.Name]
			if !ok {
				return errNotFound
			}
			svc.Gate = probe.Gate
			svc.GateSecret = probe.GateSecret
			svc.GateAllowPaths = probe.GateAllowPaths
			svc.GateAllowIPs = probe.GateAllowIPs
			svc.GateAllowBots = probe.GateAllowBots
			c.Services[body.Name] = svc
			return nil
		}); err != nil {
			writeErr(w, 500, "service created but protection could not be stored: "+err.Error())
			return
		}
	}
	out := applied(s.autoApply())
	out["name"] = body.Name
	writeJSON(w, 200, out)
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
			p := config.NormalizePath(*body.Path)
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
		if body.Target != nil {
			t := strings.TrimSpace(*body.Target)
			if config.IsLocalTarget(t) {
				t = ""
			}
			svc.Target = t
		}
		// Only touch the gate when the caller actually sent a mode. A PATCH
		// that omits it must leave the protection exactly as it was.
		if body.GateAllowPaths != nil {
			svc.GateAllowPaths = *body.GateAllowPaths
		}
		if body.GateAllowIPs != nil {
			svc.GateAllowIPs = *body.GateAllowIPs
		}
		if body.GateAllowBots != nil {
			svc.GateAllowBots = *body.GateAllowBots
		}
		if body.Disabled != nil {
			svc.Disabled = *body.Disabled
		}
		if body.Gate != nil {
			if err := applyGate(&svc, *body.Gate, body.GateSecret); err != nil {
				return err
			}
		} else if body.GateSecret != nil && config.NormalizeGate(svc.Gate) == config.GateSecret {
			// Rotating the key without changing the mode.
			if err := applyGate(&svc, config.GateSecret, body.GateSecret); err != nil {
				return err
			}
		}
		if err := validateGateExceptions(svc); err != nil {
			return err
		}
		// Changing a path, a port or the enabled flag can move this service
		// on top of another one. Checked before the write, so a rejected
		// edit leaves the stored config exactly as it was.
		if err := config.CheckPathConflict(c, name, svc); err != nil {
			return err
		}
		c.Services[name] = svc
		return nil
	})
	if err != nil {
		if err == errNotFound {
			writeErr(w, 404, "Service not found")
		} else {
			writeServiceErr(w, 400, err)
		}
		return
	}
	writeJSON(w, 200, applied(s.autoApply()))
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
	writeJSON(w, 200, applied(s.autoApply()))
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
	writeJSON(w, 200, applied(s.autoApply()))
}

// handleSetBindings REPLACES the whole binding list. Editing a service needs
// this: adding a binding would leave the old one behind and the service would
// answer on a hostname the operator just removed.
func (s *Server) handleSetBindings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Bindings []config.Binding `json:"bindings"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	for _, b := range body.Bindings {
		if strings.TrimSpace(b.Domain) == "" {
			writeErr(w, 400, "every binding needs a domain")
			return
		}
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		svc, ok := c.Services[name]
		if !ok {
			return os.ErrNotExist
		}
		for _, b := range body.Bindings {
			if _, ok := c.Domains[b.Domain]; !ok {
				return fmt.Errorf("domain %s not found", b.Domain)
			}
		}
		svc.Bindings = body.Bindings
		// Replacing the binding list can both collide with another service
		// and make the service collide with itself, if two bindings resolve
		// to the same hostname.
		if err := config.SelfPathConflict(c, name, svc); err != nil {
			return err
		}
		if err := config.CheckPathConflict(c, name, svc); err != nil {
			return err
		}
		c.Services[name] = svc
		return nil
	})
	if err != nil {
		if isNotExist(err) {
			writeErr(w, 404, "Service not found")
		} else {
			writeServiceErr(w, 400, err)
		}
		return
	}
	writeJSON(w, 200, applied(s.autoApply()))
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
	writeJSON(w, 200, applied(s.autoApply()))
}

func intInSlice(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
