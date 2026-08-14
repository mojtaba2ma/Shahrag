package nginx

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const confPath = "/etc/nginx/nginx.conf"

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
	txt, err := os.ReadFile(confPath)
	if err != nil {
		return err
	}
	s := string(txt)
	if regexp.MustCompile(`worker_connections\s+\d+`).MatchString(s) {
		s = regexp.MustCompile(`worker_connections\s+\d+`).ReplaceAllString(s, fmt.Sprintf("worker_connections %d", n))
	} else {
		s = regexp.MustCompile(`(?s)(events\s*\{)`).ReplaceAllString(s,
			fmt.Sprintf("${1}\n    worker_connections %d;", n))
	}
	return os.WriteFile(confPath, []byte(s), 0o644)
}

// CacheEnabled reports whether proxy_cache has been disabled via "proxy_cache off;".
// Returns true when cache is ENABLED (i.e. no "proxy_cache off" line).
func CacheEnabled() bool {
	txt, err := os.ReadFile(confPath)
	if err != nil {
		return true
	}
	return !strings.Contains(string(txt), "proxy_cache off")
}

// SetCache enables or disables proxy cache.
func SetCache(enabled bool) error {
	txt, err := os.ReadFile(confPath)
	if err != nil {
		return err
	}
	s := string(txt)
	if enabled {
		s = strings.ReplaceAll(s, "    proxy_cache off;\n", "")
	} else if !strings.Contains(s, "proxy_cache off") {
		s = regexp.MustCompile(`(?m)(^|\n)(\s*http\s*\{)`).
			ReplaceAllString(s, "${1}${2}\n    proxy_cache off;")
	}
	return os.WriteFile(confPath, []byte(s), 0o644)
}

// LogLevel returns the current error_log level.
func LogLevel() string {
	txt, _ := os.ReadFile(confPath)
	m := regexp.MustCompile(`error_log\s+\S+\s+(\w+)`).FindStringSubmatch(string(txt))
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
	txt, err := os.ReadFile(confPath)
	if err != nil {
		return err
	}
	s := string(txt)
	if regexp.MustCompile(`error_log\s+\S+\s+\w+`).MatchString(s) {
		s = regexp.MustCompile(`(error_log\s+\S+\s+)\w+`).ReplaceAllString(s, "${1}"+level)
	}
	return os.WriteFile(confPath, []byte(s), 0o644)
}

// EnableStubStatus adds a stub_status server block for connection metrics.
func EnableStubStatus() error {
	txt, err := os.ReadFile(confPath)
	if err != nil {
		return nil
	}
	s := string(txt)
	if strings.Contains(s, "stub_status") {
		return nil
	}
	snippet := `
# Shahrag stub_status
server {
    listen 127.0.0.1:80;
    server_name _;
    location = /nginx_status {
        stub_status;
        access_log off;
        allow 127.0.0.1;
        deny all;
    }
}
`
	httpRe := regexp.MustCompile(`(?m)(http\s*\{)`)
	if httpRe.MatchString(s) {
		s = httpRe.ReplaceAllString(s, "${1}"+snippet)
	}
	return os.WriteFile(confPath, []byte(s), 0o644)
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
