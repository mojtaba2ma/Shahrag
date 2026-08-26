package web

// Viewing and editing the raw configuration files.
//
// Two files decide whether the server works: /etc/nginx-panel/config.json
// (what the panel knows) and the generated nginx config (what nginx actually
// serves). Being able to READ them from the panel removes most of the
// guesswork when something looks wrong, and being able to EDIT them turns a
// "please SSH in and fix line 42" into a two-minute job.
//
// Editing raw files is inherently dangerous, so every write here is
// transactional in exactly the way the generator is:
//
//   - the file is snapshotted first;
//   - JSON is parsed before it is written, nginx files are validated with
//     `nginx -t` after being written;
//   - anything that fails restores the snapshot, so a bad edit can never
//     leave the server with a config it cannot start from.
//
// The gateway files are GENERATED. Editing them by hand is still useful for
// an urgent fix, but the next `generate` overwrites them — the API says so
// explicitly rather than letting the operator discover it later.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

// editableFile describes one file the panel can show and (optionally) write.
type editableFile struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Language    string `json:"language"` // "json" | "nginx"
	Label       string `json:"label"`
	Editable    bool   `json:"editable"`
	Generated   bool   `json:"generated"` // rewritten by `shahrag generate`
	Description string `json:"description"`
}

// knownFiles returns the files the panel exposes, resolved from the live
// config so a custom output path is honoured.
func (s *Server) knownFiles() []editableFile {
	gateway := nginxpkg.GatewayPath()
	stream := nginxpkg.StreamPath()
	if c, err := s.cfg.Read(); err == nil {
		if c.Nginx.OutputPath != "" {
			gateway = c.Nginx.OutputPath
		}
		if c.Nginx.StreamOutputPath != "" {
			stream = c.Nginx.StreamOutputPath
		}
	}
	return []editableFile{
		{
			ID: "config", Path: config.ConfigPath, Language: "json",
			Label: "config.json", Editable: true, Generated: false,
			Description: "The panel's own configuration. Editing it changes what the panel knows; run Generate afterwards to apply it to nginx.",
		},
		{
			ID: "gateway", Path: gateway, Language: "nginx",
			Label: "gateway.conf", Editable: true, Generated: true,
			Description: "The generated HTTP configuration. Hand edits are validated and applied, but the next Generate overwrites this file.",
		},
		{
			ID: "stream", Path: stream, Language: "nginx",
			Label: "stream-gateway.conf", Editable: true, Generated: true,
			Description: "The generated stream (SNI routing) configuration. The next Generate overwrites this file.",
		},
		{
			ID: "nginxconf", Path: nginxpkg.DefaultNginxConf, Language: "nginx",
			Label: "nginx.conf", Editable: true, Generated: false,
			Description: "The main nginx configuration. Shahrag only touches its marked stream include.",
		},
	}
}

func (s *Server) fileByID(id string) (editableFile, bool) {
	for _, f := range s.knownFiles() {
		if f.ID == id {
			return f, true
		}
	}
	return editableFile{}, false
}

// handleListFiles reports which files exist, their size and whether they are
// generated, so the UI can label them before anything is opened.
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		editableFile
		Exists bool  `json:"exists"`
		Size   int64 `json:"size"`
	}
	out := []entry{}
	for _, f := range s.knownFiles() {
		e := entry{editableFile: f}
		if st, err := os.Stat(f.Path); err == nil {
			e.Exists = true
			e.Size = st.Size()
		}
		out = append(out, e)
	}
	writeJSON(w, 200, out)
}

// handleGetFile returns one file's contents as plain text.
func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "Unknown file")
		return
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, 200, map[string]interface{}{
				"id": f.ID, "path": f.Path, "language": f.Language,
				"content": "", "exists": false, "generated": f.Generated,
				"description": f.Description,
			})
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"id": f.ID, "path": f.Path, "language": f.Language,
		"content": string(data), "exists": true, "generated": f.Generated,
		"description": f.Description,
	})
}

type fileWriteReq struct {
	Content string `json:"content"`
	// Reload applies the change to the running nginx after validation.
	Reload bool `json:"reload"`
}

// handleSaveFile writes a file transactionally: snapshot, validate, restore
// on failure. A syntactically broken config can therefore never survive a
// save, which is the whole reason editing raw files from a web UI is safe
// enough to offer at all.
func (s *Server) handleSaveFile(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "Unknown file")
		return
	}
	if !f.Editable {
		writeErr(w, 403, "This file is read-only")
		return
	}
	var body fileWriteReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeErr(w, 400, "Refusing to write an empty file")
		return
	}

	// JSON is checked BEFORE writing: an unparsable config.json would leave
	// the panel unable to read its own state.
	if f.Language == "json" {
		var probe config.Config
		if err := json.Unmarshal([]byte(body.Content), &probe); err != nil {
			writeErr(w, 400, "Invalid JSON: "+err.Error())
			return
		}
		if probe.Domains == nil || probe.Services == nil {
			writeErr(w, 400, "This does not look like a Shahrag config (missing domains/services)")
			return
		}
	}

	backup, err := nginxpkg.SnapBackup(f.Path)
	if err != nil {
		writeErr(w, 500, "cannot snapshot the file before writing: "+err.Error())
		return
	}

	// Keep a timestamped copy as well, so an edit can be recovered later and
	// not just rolled back within this request.
	s.archiveBeforeEdit(f)

	mode := os.FileMode(0o644)
	if f.Language == "json" {
		mode = 0o600 // the config holds the session secret and password hash
	}
	if st, err := os.Stat(f.Path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := os.WriteFile(f.Path, []byte(body.Content), mode); err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	resp := map[string]interface{}{"ok": true, "path": f.Path}

	// nginx files are validated by nginx itself, after the write, because
	// `nginx -t` reads the whole tree from disk.
	if f.Language == "nginx" {
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
	}

	// A hand-edited config.json is only half applied until nginx is
	// regenerated from it; do that when asked.
	if f.Language == "json" && body.Reload {
		res, gerr := s.gen.GenerateAndReload()
		resp["generate"] = res
		if gerr != nil {
			resp["ok"] = false
			resp["detail"] = "config saved, but generating nginx failed: " + gerr.Error()
		} else if ok, _ := res["ok"].(bool); !ok {
			resp["ok"] = false
			resp["detail"] = "config saved, but nginx rejected the generated files (they were rolled back)"
		}
	}

	if f.Generated {
		resp["warning"] = "This file is generated: the next Generate will overwrite these edits."
	}
	writeJSON(w, 200, resp)
}

// nowStamp is the timestamp format used for edit archives.
func nowStamp() string { return time.Now().Format("20060102-150405") }

// archiveBeforeEdit keeps a timestamped copy of a file about to be edited.
func (s *Server) archiveBeforeEdit(f editableFile) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return
	}
	dir := filepath.Join("/var/backups/shahrag", "edits")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	name := fmt.Sprintf("%s-%s%s", f.ID, nowStamp(), filepath.Ext(f.Path))
	_ = os.WriteFile(filepath.Join(dir, name), data, 0o600)
}
