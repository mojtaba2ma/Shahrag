package nginx

// Boot resilience for nginx.
//
// The problem this file solves (reported after a server reboot: "nginx is
// inactive but `nginx -t` says the config is valid"):
//
//   - Debian/Ubuntu ship nginx.service WITHOUT Restart=. If the very first
//     start attempt at boot fails for a transient reason — a listen port not
//     yet released, IPv6 not up yet (`listen [::]:6038` → EADDRNOTAVAIL),
//     another daemon (xray/x-ui) winning the race for a Reality port, or a
//     certificate on a filesystem that is not mounted yet — systemd gives up
//     permanently. The config on disk is perfectly valid, which is exactly
//     what the user observed.
//   - nginx.service is ordered only After=network.target, which does NOT mean
//     "addresses are configured". network-online.target does.
//   - worker_connections was raised to 65536 while nginx.service keeps the
//     default file-descriptor limit, so nginx logs
//     "worker_connections exceed open file resource limit" on every start.
//
// Shahrag therefore installs a small, clearly-marked systemd drop-in for
// nginx (never touching the distribution's unit file) and runs a watchdog
// inside the panel process that brings nginx back up when it is down while
// the configuration is valid — unless the operator stopped it deliberately
// through the panel/CLI.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DropInDir is the systemd drop-in directory for nginx.service.
// Overridable via SHAHRAG_NGINX_DROPIN_DIR (tests).
var DropInDir = envOrDefault("SHAHRAG_NGINX_DROPIN_DIR", "/etc/systemd/system/nginx.service.d")

// DropInPath is the single drop-in file Shahrag owns. Everything in it is
// managed by Shahrag and safe to overwrite; the distribution unit file is
// never modified.
func DropInPath() string { return filepath.Join(DropInDir, "shahrag-resilience.conf") }

func envOrDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// StopFlagPath marks "nginx was stopped on purpose". While it exists the
// watchdog leaves nginx alone, so an operator can stop nginx for maintenance
// without the panel fighting them. Living under /run it disappears on reboot,
// which is what we want: a reboot must always bring nginx back.
var StopFlagPath = envOrDefault("SHAHRAG_NGINX_STOPFLAG", "/run/shahrag-nginx-stopped")

// dropInContent is the drop-in Shahrag installs.
//
// Restart=on-failure + StartLimitIntervalSec=0 is the important part: systemd
// keeps retrying forever instead of giving up after 5 attempts, so a boot-time
// race (port still held, network not ready) heals itself within seconds
// instead of leaving the server offline until someone logs in.
const dropInContent = `# Managed by Shahrag — do not edit; regenerate with: shahrag boot-guard
#
# Keeps nginx up across reboots and transient start failures.
[Unit]
# network.target only means "the network stack exists"; addresses may not be
# configured yet, which breaks ` + "`listen [::]:port`" + ` at boot.
After=network-online.target
Wants=network-online.target
# Never stop retrying (systemd's default start-limit would give up).
StartLimitIntervalSec=0

[Service]
# Debian/Ubuntu ship nginx without Restart=, so a single failed boot attempt
# leaves the server permanently down.
Restart=on-failure
RestartSec=3s
TimeoutStartSec=60s
# worker_connections is often raised well above the default fd limit.
LimitNOFILE=65535
`

// InstallDropIn writes (or refreshes) the systemd drop-in and reloads the
// systemd manager. It returns true when the file changed.
func InstallDropIn() (bool, error) {
	if err := os.MkdirAll(DropInDir, 0o755); err != nil {
		return false, err
	}
	if cur, err := os.ReadFile(DropInPath()); err == nil && string(cur) == dropInContent {
		return false, nil
	}
	if err := os.WriteFile(DropInPath(), []byte(dropInContent), 0o644); err != nil {
		return false, err
	}
	if os.Getenv("SHAHRAG_NGINX_DROPIN_DIR") == "" {
		if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
			return true, fmt.Errorf("systemctl daemon-reload: %w", err)
		}
	}
	return true, nil
}

// DropInInstalled reports whether the drop-in is present and current.
func DropInInstalled() bool {
	cur, err := os.ReadFile(DropInPath())
	return err == nil && string(cur) == dropInContent
}

// IsEnabled reports whether nginx.service is enabled (starts at boot). A
// disabled nginx is the simplest possible cause of "gone after a reboot".
func IsEnabled() bool {
	out, _ := exec.Command("systemctl", "is-enabled", "nginx").Output()
	s := strings.TrimSpace(string(out))
	return s == "enabled" || s == "enabled-runtime" || s == "static" || s == "indirect"
}

// EnsureEnabled enables nginx.service when it is not enabled.
func EnsureEnabled() error {
	if IsEnabled() {
		return nil
	}
	return exec.Command("systemctl", "enable", "nginx").Run()
}

// MarkStopped / ClearStopped record an intentional stop so the watchdog does
// not immediately restart nginx behind the operator's back.
func MarkStopped() {
	_ = os.WriteFile(StopFlagPath, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
}
func ClearStopped() {
	_ = os.Remove(StopFlagPath)
}
func stoppedOnPurpose() bool {
	_, err := os.Stat(StopFlagPath)
	return err == nil
}

// BootGuard applies every boot-safety measure once, at panel start:
// install the drop-in, enable the unit, and start nginx if it is down while
// the configuration is valid. It returns a human-readable list of the
// actions it took.
func BootGuard(g *Generator) []string {
	var actions []string

	// Nothing to guard when nginx is not installed here (e.g. a dev box).
	if NginxBinary() == "" {
		return nil
	}

	if changed, err := InstallDropIn(); err != nil {
		actions = append(actions, "drop-in install failed: "+err.Error())
	} else if changed {
		actions = append(actions, "installed systemd drop-in "+DropInPath())
	}

	if !IsEnabled() {
		if err := EnsureEnabled(); err != nil {
			actions = append(actions, "could not enable nginx.service: "+err.Error())
		} else {
			actions = append(actions, "enabled nginx.service (it will now start at boot)")
		}
	}

	// After a reboot an intentional stop must not survive: /run is cleared,
	// but be explicit for the case where /run is persistent.
	if bootedRecently() {
		ClearStopped()
	}

	if msg := recoverNginx(g); msg != "" {
		actions = append(actions, msg)
	}
	return actions
}

// bootedRecently reports whether the machine booted less than 5 minutes ago.
func bootedRecently() bool {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return false
	}
	var up float64
	if _, err := fmt.Sscanf(string(data), "%f", &up); err != nil {
		return false
	}
	return up < 300
}

// recoverNginx starts nginx when it is down, the config is valid and the stop
// was not intentional. It returns a description of what happened ("" = nothing
// to do).
func recoverNginx(g *Generator) string {
	msg, _ := recoverNginxState(g)
	return msg
}

// recoverNginxState is recoverNginx plus a STABLE state key. The message
// itself embeds nginx's own timestamps and PIDs, so comparing messages to
// suppress duplicate log lines never worked — the watchdog flooded the
// journal with one near-identical line every 30 seconds.
func recoverNginxState(g *Generator) (msg, state string) {
	if IsActive() {
		return "", "active"
	}
	if stoppedOnPurpose() {
		return "", "stopped-on-purpose"
	}
	if NginxBinary() == "" {
		return "", "not-installed"
	}
	t := g.Test()
	if !t.OK {
		// A broken config must never be "fixed" by restart loops — say so
		// loudly instead so doctor/logs show the real reason.
		return "nginx is down and its configuration is INVALID — not starting it: " +
			strings.TrimSpace(firstLine(t.Stderr)), "invalid-config"
	}
	// `nginx -t` only PARSES the config — it never binds a socket. When
	// another daemon (xray, x-ui, sing-box…) already owns a port nginx is
	// configured to listen on, the test passes but the start fails with
	// "bind() ... (98: Address already in use)". Detect that first so the
	// report names the culprit instead of leaving a bare "inactive".
	if cs := FindPortConflicts(GeneratedFiles(GatewayPath(), StreamPath())...); len(cs) > 0 {
		return "nginx cannot start — " + strings.Join(DescribeConflicts(cs), "; "), "port-conflict"
	}

	out, err := exec.Command("systemctl", "start", "nginx").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if reason := LastFailureReason(); reason != "" {
			detail = strings.TrimSpace(detail + " | " + reason)
		}
		// Re-check after the failure: the conflict may have appeared
		// between the pre-check and the start attempt.
		if cs := FindPortConflicts(GeneratedFiles(GatewayPath(), StreamPath())...); len(cs) > 0 {
			detail += " | " + strings.Join(DescribeConflicts(cs), "; ")
		}
		return "nginx is down and `systemctl start nginx` failed: " + detail, "start-failed"
	}
	return "nginx was down with a valid config — started it", "started"
}

// Watchdog periodically re-checks nginx and brings it back when it is down
// with a valid configuration. It runs for the lifetime of the panel process
// and logs through the provided function.
func Watchdog(g *Generator, every time.Duration, logf func(string, ...interface{})) {
	if every <= 0 {
		every = 30 * time.Second
	}
	// Repeating the same line every 30s would flood the journal; log a
	// message only when the situation CHANGES.
	last := ""
	for {
		time.Sleep(every)
		msg, state := recoverNginxState(g)
		// Compare the STATE, not the message: nginx embeds a timestamp and
		// PID in its output, so every message differs and the journal used
		// to get one line every 30 seconds forever.
		if msg != "" && state != last {
			logf("nginx watchdog: %s", msg)
		}
		if state == "started" {
			logf("nginx watchdog: nginx is healthy again")
		}
		last = state
	}
}

// LastFailureReason returns the most useful lines from the nginx journal and
// error log, so doctor can explain WHY nginx did not come up at boot instead
// of just reporting "inactive".
func LastFailureReason() string {
	var parts []string
	if out, err := exec.Command("journalctl", "-u", "nginx", "--no-pager", "-n", "25").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			low := strings.ToLower(l)
			if strings.Contains(low, "failed") || strings.Contains(low, "emerg") ||
				strings.Contains(low, "address already in use") || strings.Contains(low, "cannot") ||
				strings.Contains(low, "no such file") || strings.Contains(low, "permission denied") {
				parts = append(parts, strings.TrimSpace(l))
			}
		}
	}
	if data, err := os.ReadFile("/var/log/nginx/error.log"); err == nil {
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		for _, l := range lines {
			if strings.Contains(l, "[emerg]") {
				parts = append(parts, strings.TrimSpace(l))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 6 {
		parts = parts[len(parts)-6:]
	}
	return strings.Join(parts, "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// WorkerRLimit reads worker_rlimit_nofile from nginx.conf (0 = not set).
func WorkerRLimit() int {
	txt, _ := os.ReadFile(confPath)
	m := reWorkerRLimit.FindStringSubmatch(string(txt))
	if m == nil {
		return 0
	}
	var n int
	fmt.Sscanf(m[1], "%d", &n)
	return n
}

// EnsureWorkerRLimit makes sure worker_rlimit_nofile is at least as large as
// worker_connections. Without it nginx logs
// "worker_connections exceed open file resource limit" on every start and
// silently caps the number of connections it can actually serve.
func EnsureWorkerRLimit(workerConnections int) error {
	if workerConnections <= 1024 {
		return nil
	}
	if WorkerRLimit() >= workerConnections {
		return nil
	}
	want := workerConnections
	if want > 65535 {
		want = 65535
	}
	return editNginxConf(func(s string) string {
		if reWorkerRLimit.MatchString(s) {
			return reWorkerRLimit.ReplaceAllString(s, fmt.Sprintf("worker_rlimit_nofile %d", want))
		}
		// Insert at top level, right after the worker_processes line.
		if reWorkerProcesses.MatchString(s) {
			return reWorkerProcesses.ReplaceAllString(s,
				fmt.Sprintf("${0}\nworker_rlimit_nofile %d;", want))
		}
		return fmt.Sprintf("worker_rlimit_nofile %d;\n", want) + s
	})
}

// SystemdDropInSummary is a short description used by doctor.
func SystemdDropInSummary() string {
	if !DropInInstalled() {
		return "missing (run: sudo shahrag boot-guard)"
	}
	return filepath.Base(DropInPath()) + " installed (Restart=on-failure, After=network-online.target)"
}

// NginxBinary resolves the nginx executable. systemd units get a minimal
// PATH, and some distributions keep nginx only in /usr/sbin, so a plain
// exec.Command("nginx") can fail with "executable file not found" and make
// a perfectly healthy server look broken.
func NginxBinary() string {
	if p, err := exec.LookPath("nginx"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/sbin/nginx", "/usr/local/sbin/nginx", "/sbin/nginx", "/usr/local/nginx/sbin/nginx"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// GatewayPath / StreamPath return the generated file locations, honouring
// the env overrides used by tests.
func GatewayPath() string {
	return envOrDefault("SHAHRAG_GATEWAY_CONF", "/etc/nginx/conf.d/gateway.conf")
}

func StreamPath() string {
	return envOrDefault("SHAHRAG_STREAM_CONF", "/etc/nginx/stream-gateway.conf")
}
