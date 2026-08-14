package nginx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const confPath = "/etc/nginx/nginx.conf"

// confDDir is the drop-in directory included inside http{} on Debian/Ubuntu
// nginx setups. When it exists and is included by nginx.conf we prefer
// writing drop-in files over editing nginx.conf directly.
const confDDir = "/etc/nginx/conf.d"

// confDIncluded reports whether nginx.conf includes conf.d/*.conf inside the
// http context (the default layout on Debian/Ubuntu).
func confDIncluded() bool {
	txt, err := os.ReadFile(confPath)
	if err != nil {
		return false
	}
	re := regexp.MustCompile(`(?s)http\s*\{.*?include\s+[^;]*conf\.d/\*\.conf\s*;`)
	return re.Match(txt)
}

// editNginxConf runs fn (which must edit nginx.conf) and validates the result
// with `nginx -t`, restoring the previous content if the test fails.
func editNginxConf(fn func(txt string) string) error {
	txtB, err := os.ReadFile(confPath)
	if err != nil {
		return err
	}
	before := string(txtB)
	after := fn(before)
	if after == before {
		return nil
	}
	bak, err := SnapBackup(confPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(confPath, []byte(after), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("nginx", "-t")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = bak.Restore()
		return fmt.Errorf("nginx -t rejected the change (rolled back): %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// IsActive reports whether nginx.service is active.
func IsActive() bool {
	out, err := exec.Command("systemctl", "is-active", "nginx").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}

// Version returns the nginx version string (from `nginx -v`).
func Version() string {
	cmd := exec.Command("nginx", "-v")
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

// WorkerConnections reads the current worker_connections value.
func WorkerConnections() int {
	txt, _ := os.ReadFile(confPath)
	m := regexp.MustCompile(`worker_connections\s+(\d+)`).FindStringSubmatch(string(txt))
	if m == nil {
		return 0
	}
	var n int
	fmt.Sscanf(m[1], "%d", &n)
	return n
}

// SetWorkerConnections updates worker_connections in nginx.conf.
func SetWorkerConnections(n int) error {
	if n < 1 || n > 65536 {
		return fmt.Errorf("worker_connections must be 1..65536")
	}
	return editNginxConf(func(s string) string {
		if regexp.MustCompile(`worker_connections\s+\d+`).MatchString(s) {
			return regexp.MustCompile(`worker_connections\s+\d+`).ReplaceAllString(s, fmt.Sprintf("worker_connections %d", n))
		}
		return regexp.MustCompile(`(?s)(events\s*\{)`).ReplaceAllString(s,
			fmt.Sprintf("${1}\n    worker_connections %d;", n))
	})
}

// CacheEnabled reports whether proxy_cache has been disabled via "proxy_cache off;".
// Returns true when cache is ENABLED (i.e. no "proxy_cache off" line).
func CacheEnabled() bool {
	txt, err := os.ReadFile(confPath)
	if err != nil {
		return true
	}
	if strings.Contains(string(txt), "proxy_cache off") {
		return false
	}
	drop := filepath.Join(confDDir, "shahrag-cache.conf")
	if data, err := os.ReadFile(drop); err == nil && strings.Contains(string(data), "proxy_cache off") {
		return false
	}
	return true
}

// SetCache enables or disables proxy cache. With the standard conf.d layout a
// drop-in file is used instead of editing nginx.conf; otherwise nginx.conf is
// edited with validation and rollback.
func SetCache(enabled bool) error {
	drop := filepath.Join(confDDir, "shahrag-cache.conf")
	if enabled {
		// Remove our drop-in (ignore when absent).
		if err := os.Remove(drop); err != nil && !os.IsNotExist(err) {
			return err
		}
		// Also remove any inline "proxy_cache off" that earlier versions added.
		return editNginxConf(func(s string) string {
			return strings.ReplaceAll(s, "    proxy_cache off;\n", "")
		})
	}

	if confDIncluded() {
		if err := os.MkdirAll(confDDir, 0o755); err != nil {
			return err
		}
		content := "# Shahrag: proxy_cache disabled (matches CLI behaviour).\nproxy_cache off;\n"
		bak, err := SnapBackup(drop)
		if err != nil {
			return err
		}
		if err := os.WriteFile(drop, []byte(content), 0o644); err != nil {
			return err
		}
		cmd := exec.Command("nginx", "-t")
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = bak.Restore()
			return fmt.Errorf("nginx -t rejected the cache drop-in (rolled back): %s", strings.TrimSpace(string(out)))
		}
		return nil
	}

	// No conf.d include — edit nginx.conf inline with rollback.
	return editNginxConf(func(s string) string {
		if strings.Contains(s, "proxy_cache off") {
			return s
		}
		return regexp.MustCompile(`(?m)(^|\n)(\s*http\s*\{)`).
			ReplaceAllString(s, "${1}${2}\n    proxy_cache off;")
	})
}

// LogLevel returns the current error_log level.
func LogLevel() string {
	txt, _ := os.ReadFile(confPath)
	// [ \t] instead of \s so the match can never cross into the next line.
	m := regexp.MustCompile(`(?m)^[ \t]*error_log[ \t]+\S+[ \t]+(\w+)`).FindStringSubmatch(string(txt))
	if m == nil {
		return "warn"
	}
	return m[1]
}

// SetLogLevel updates the error_log level.
func SetLogLevel(level string) error {
	valid := map[string]bool{
		"debug": true, "info": true, "notice": true, "warn": true,
		"error": true, "crit": true, "alert": true, "emerg": true,
	}
	if !valid[level] {
		return fmt.Errorf("invalid log level: %s", level)
	}
	return editNginxConf(func(s string) string {
		// [ \t] only — \s would match newlines and corrupt the line after
		// an error_log directive that has no level.
		re := regexp.MustCompile(`(?m)(^[ \t]*error_log[ \t]+\S+[ \t]+)\w+`)
		if re.MatchString(s) {
			return re.ReplaceAllString(s, "${1}"+level)
		}
		return s
	})
}

// stubDropInPath is the drop-in file for the stub_status metrics endpoint.
const stubDropInPath = "/etc/nginx/conf.d/shahrag-stub.conf"

// stubDropInContent is the standard drop-in (127.0.0.1:8081 so the stats
// collector can always reach it).
const stubDropInContent = `# Shahrag connection metrics — drop-in, included inside http {}.
server {
    listen 127.0.0.1:8081;
    server_name _;
    location = /nginx_status {
        stub_status;
        access_log off;
        allow 127.0.0.1;
        deny all;
    }
}
`

// EnableStubStatus adds a stub_status server block for connection metrics.
// Prefers a conf.d drop-in (never edits nginx.conf); falls back to an inline
// edit with validation and rollback when the conf.d include is absent.
func EnableStubStatus() error {
	txt, err := os.ReadFile(confPath)
	if err != nil {
		return nil // no nginx on this host — nothing to do
	}
	s := string(txt)
	if strings.Contains(s, "stub_status") {
		return nil
	}
	if confDIncluded() {
		if err := os.MkdirAll(confDDir, 0o755); err != nil {
			return err
		}
		bak, err := SnapBackup(stubDropInPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(stubDropInPath, []byte(stubDropInContent), 0o644); err != nil {
			return err
		}
		cmd := exec.Command("nginx", "-t")
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = bak.Restore()
			return fmt.Errorf("nginx -t rejected the stub_status drop-in (rolled back): %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	// Fallback: inline into http{} with rollback on failure.
	snippet := `
# Shahrag stub_status
server {
    listen 127.0.0.1:8081;
    server_name _;
    location = /nginx_status {
        stub_status;
        access_log off;
        allow 127.0.0.1;
        deny all;
    }
}
`
	return editNginxConf(func(cur string) string {
		if strings.Contains(cur, "stub_status") {
			return cur
		}
		httpRe := regexp.MustCompile(`(?m)(http\s*\{)`)
		if httpRe.MatchString(cur) {
			return httpRe.ReplaceAllString(cur, "${1}"+snippet)
		}
		return cur
	})
}

// TailLog returns the last `n` lines of a log file.
func TailLog(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "# Log file not found: " + path
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
