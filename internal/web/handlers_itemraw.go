package web

// Per-item raw config: the JSON and the generated nginx for ONE service or
// ONE SNI rule.
//
// The whole-file editor already exists, but finding the three blocks that
// belong to one service inside a 400-line gateway.conf is exactly the kind of
// work a panel should do for you. These endpoints scope both views to a
// single record, and keep the same transactional guarantees as everywhere
// else: snapshot, validate, restore on failure.

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

// itemPaths resolves the generated file locations from the live config.
func (s *Server) itemPaths() (gateway, stream string) {
	gateway = nginxpkg.GatewayPath()
	stream = nginxpkg.StreamPath()
	if c, err := s.cfg.Read(); err == nil {
		if c.Nginx.OutputPath != "" {
			gateway = c.Nginx.OutputPath
		}
		if c.Nginx.StreamOutputPath != "" {
			stream = c.Nginx.StreamOutputPath
		}
	}
	return
}

// handleGetServiceRaw returns one service's JSON plus its generated nginx.
func (s *Server) handleGetServiceRaw(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c, err := s.cfg.Read()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	svc, ok := c.Services[name]
	if !ok {
		writeErr(w, 404, "Service not found")
		return
	}
	jsonBytes, _ := json.MarshalIndent(svc, "", "  ")

	gwPath, _ := s.itemPaths()
	nginxText, blocks := "", 0
	if data, err := os.ReadFile(gwPath); err == nil {
		nginxText, blocks = nginxpkg.ExtractServiceNginx(string(data), name)
	}
	writeJSON(w, 200, map[string]interface{}{
		"name":   name,
		"kind":   "http",
		"json":   string(jsonBytes),
		"nginx":  nginxText,
		"blocks": blocks,
		"file":   gwPath,
	})
}

// handleGetSNIRaw returns one SNI rule's JSON plus its generated map entry.
func (s *Server) handleGetSNIRaw(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c, err := s.cfg.Read()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	svc, ok := c.Reality.Services[name]
	if !ok {
		writeErr(w, 404, "SNI rule not found")
		return
	}
	jsonBytes, _ := json.MarshalIndent(svc, "", "  ")

	_, stPath := s.itemPaths()
	nginxText, blocks := "", 0
	if data, err := os.ReadFile(stPath); err == nil {
		if txt, ok := nginxpkg.ExtractSNINginx(string(data), name); ok {
			nginxText, blocks = txt, 1
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"name":   name,
		"kind":   "sni",
		"json":   string(jsonBytes),
		"nginx":  nginxText,
		"blocks": blocks,
		"file":   stPath,
	})
}

type itemRawReq struct {
	// Exactly one of these is applied per request; the UI saves whichever
	// tab the operator edited.
	JSON  *string `json:"json"`
	Nginx *string `json:"nginx"`
	// Reload applies the change to the running nginx after validation.
	Reload bool `json:"reload"`
}

// handleSaveServiceRaw writes an edited service (JSON and/or its nginx).
func (s *Server) handleSaveServiceRaw(w http.ResponseWriter, r *http.Request) {
	s.saveItemRaw(w, r, false)
}

// handleSaveSNIRaw writes an edited SNI rule (JSON and/or its map entry).
func (s *Server) handleSaveSNIRaw(w http.ResponseWriter, r *http.Request) {
	s.saveItemRaw(w, r, true)
}

func (s *Server) saveItemRaw(w http.ResponseWriter, r *http.Request, isSNI bool) {
	name := r.PathValue("name")
	var body itemRawReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if body.JSON == nil && body.Nginx == nil {
		writeErr(w, 400, "Nothing to save")
		return
	}

	resp := map[string]interface{}{"ok": true}

	// ── 1. The record's JSON ──────────────────────────────────────
	if body.JSON != nil {
		if strings.TrimSpace(*body.JSON) == "" {
			writeErr(w, 400, "Refusing to save an empty record")
			return
		}
		if isSNI {
			var svc config.RealityService
			if err := json.Unmarshal([]byte(*body.JSON), &svc); err != nil {
				writeErr(w, 400, "Invalid JSON: "+err.Error())
				return
			}
			if strings.TrimSpace(svc.SNI) == "" {
				writeErr(w, 400, "An SNI rule needs an sni value")
				return
			}
			if _, err := s.cfg.Mutate(func(c *config.Config) error {
				if _, ok := c.Reality.Services[name]; !ok {
					return os.ErrNotExist
				}
				c.Reality.Services[name] = svc
				return nil
			}); err != nil {
				if isNotExist(err) {
					writeErr(w, 404, "SNI rule not found")
				} else {
					writeErr(w, 500, err.Error())
				}
				return
			}
		} else {
			var svc config.Service
			if err := json.Unmarshal([]byte(*body.JSON), &svc); err != nil {
				writeErr(w, 400, "Invalid JSON: "+err.Error())
				return
			}
			if svc.LocalPort < 1 || svc.LocalPort > 65535 {
				writeErr(w, 400, "local_port must be between 1 and 65535")
				return
			}
			if len(svc.Bindings) == 0 {
				writeErr(w, 400, "A service needs at least one binding, or it generates nothing")
				return
			}
			if _, err := s.cfg.Mutate(func(c *config.Config) error {
				if _, ok := c.Services[name]; !ok {
					return os.ErrNotExist
				}
				for _, b := range svc.Bindings {
					if _, ok := c.Domains[b.Domain]; !ok {
						return errUnknownDomain{b.Domain}
					}
				}
				c.Services[name] = svc
				return nil
			}); err != nil {
				if isNotExist(err) {
					writeErr(w, 404, "Service not found")
					return
				}
				var ud errUnknownDomain
				if asUnknownDomain(err, &ud) {
					writeErr(w, 400, "Unknown domain: "+ud.domain)
					return
				}
				writeErr(w, 500, err.Error())
				return
			}
		}

		// A JSON change only reaches nginx through the generator.
		if body.Reload {
			res, gerr := s.gen.GenerateAndReload()
			resp["generate"] = res
			if gerr != nil {
				resp["ok"] = false
				resp["detail"] = "saved, but generating nginx failed: " + gerr.Error()
			} else if ok, _ := res["ok"].(bool); !ok {
				resp["ok"] = false
				resp["detail"] = "saved, but nginx rejected the generated files (they were rolled back)"
			}
		}
	}

	// ── 2. The generated nginx for this item ──────────────────────
	if body.Nginx != nil {
		gwPath, stPath := s.itemPaths()
		path := gwPath
		if isSNI {
			path = stPath
		}
		data, err := os.ReadFile(path)
		if err != nil {
			writeErr(w, 500, "cannot read "+path+": "+err.Error())
			return
		}

		var updated string
		if isSNI {
			updated, err = nginxpkg.ReplaceSNINginx(string(data), name, *body.Nginx)
		} else {
			updated, err = nginxpkg.ReplaceServiceNginx(string(data), name, *body.Nginx)
		}
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}

		backup, berr := nginxpkg.SnapBackup(path)
		if berr != nil {
			writeErr(w, 500, "cannot snapshot before writing: "+berr.Error())
			return
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.WriteFile(path, []byte(updated), mode); err != nil {
			writeErr(w, 500, err.Error())
			return
		}

		test := s.gen.Test()
		resp["test"] = test
		if !test.OK {
			_ = backup.Restore()
			writeJSON(w, 200, map[string]interface{}{
				"ok":       false,
				"restored": true,
				"detail":   "nginx rejected the change, so the previous file was restored",
				"stderr":   strings.TrimSpace(test.Stderr),
			})
			return
		}
		if body.Reload {
			rel := s.gen.Reload()
			resp["reload"] = rel
			if !rel.OK {
				_ = backup.Restore()
				_ = s.gen.Reload()
				writeJSON(w, 200, map[string]interface{}{
					"ok":       false,
					"restored": true,
					"detail":   "nginx could not be reloaded, so the previous file was restored",
					"stderr":   strings.TrimSpace(rel.Stderr),
				})
				return
			}
		}
		resp["warning"] = "This file is generated: the next Generate will overwrite these edits."
	}

	writeJSON(w, 200, resp)
}

// errUnknownDomain reports a binding that names a domain the panel does not
// know, which would silently generate nothing.
type errUnknownDomain struct{ domain string }

func (e errUnknownDomain) Error() string { return "unknown domain: " + e.domain }

func asUnknownDomain(err error, out *errUnknownDomain) bool {
	if e, ok := err.(errUnknownDomain); ok {
		*out = e
		return true
	}
	return false
}
