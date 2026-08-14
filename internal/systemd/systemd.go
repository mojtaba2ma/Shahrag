// Package systemd provides thin helpers around systemctl for the services
// Shahrag manages (its own unit and nginx).
package systemd

import (
	"os/exec"
	"strings"
)

// UnitName is the name of the Shahrag systemd unit.
const UnitName = "shahrag"

// IsActive reports whether a unit is active.
func IsActive(name string) bool {
	out, err := exec.Command("systemctl", "is-active", name).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}

// Restart restarts a unit and waits for systemd to acknowledge the request.
func Restart(name string) error {
	return exec.Command("systemctl", "restart", name).Run()
}

// Start starts a unit (no-op if it is already active).
func Start(name string) error {
	return exec.Command("systemctl", "start", name).Run()
}

// Stop stops a unit.
func Stop(name string) error {
	return exec.Command("systemctl", "stop", name).Run()
}

// Reload reloads a unit's configuration without dropping connections.
func Reload(name string) error {
	return exec.Command("systemctl", "reload", name).Run()
}

// ReloadOrStart reloads an active unit and starts it when it is not running.
// This is the safe way to apply a config change: a running nginx is never
// restarted (no dropped connections); a stopped one is started.
func ReloadOrStart(name string) error {
	if IsActive(name) {
		return Reload(name)
	}
	return Start(name)
}
