package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The drop-in is what makes nginx survive a reboot. Assert the three
// properties that matter — without them the reported outage comes back:
//   - Restart=on-failure (Debian's unit has no Restart= at all, so a single
//     failed boot attempt leaves the server down permanently);
//   - StartLimitIntervalSec=0 (otherwise systemd gives up after 5 tries);
//   - After/Wants=network-online.target (so `listen [::]:port` cannot fail
//     because IPv6 addresses are not configured yet).
func TestDropInContent(t *testing.T) {
	for _, want := range []string{
		"Restart=on-failure",
		"StartLimitIntervalSec=0",
		"After=network-online.target",
		"Wants=network-online.target",
		"LimitNOFILE=65535",
		"[Unit]",
		"[Service]",
	} {
		if !strings.Contains(dropInContent, want) {
			t.Errorf("drop-in is missing %q:\n%s", want, dropInContent)
		}
	}
	// The distribution unit file must never be touched.
	if strings.Contains(dropInContent, "ExecStart") {
		t.Error("the drop-in must not override ExecStart")
	}
}

func TestInstallDropInIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHAHRAG_NGINX_DROPIN_DIR", dir)
	old := DropInDir
	DropInDir = dir
	defer func() { DropInDir = old }()

	if DropInInstalled() {
		t.Fatal("a fresh directory must not report the drop-in as installed")
	}
	changed, err := InstallDropIn()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !changed {
		t.Error("first install must report a change")
	}
	if !DropInInstalled() {
		t.Error("drop-in must be reported as installed")
	}
	changed, err = InstallDropIn()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed {
		t.Error("second install must be a no-op")
	}
	// A drop-in from an older Shahrag version must be refreshed.
	if err := os.WriteFile(filepath.Join(dir, "shahrag-resilience.conf"), []byte("# stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if DropInInstalled() {
		t.Error("a stale drop-in must not count as installed")
	}
	if changed, _ := InstallDropIn(); !changed {
		t.Error("a stale drop-in must be rewritten")
	}
}

// The intentional-stop flag must gate the watchdog: an operator who stops
// nginx on purpose should not have the panel restart it behind their back,
// but a reboot (which clears /run) must always bring nginx back.
func TestStopFlag(t *testing.T) {
	dir := t.TempDir()
	old := StopFlagPath
	StopFlagPath = filepath.Join(dir, "stopped")
	defer func() { StopFlagPath = old }()

	if stoppedOnPurpose() {
		t.Fatal("no flag should exist initially")
	}
	MarkStopped()
	if !stoppedOnPurpose() {
		t.Error("MarkStopped must set the flag")
	}
	ClearStopped()
	if stoppedOnPurpose() {
		t.Error("ClearStopped must remove the flag")
	}
}

// EnsureWorkerRLimit must be a no-op for small values (nothing to fix) and
// must never propose more than the kernel-friendly maximum.
func TestEnsureWorkerRLimitNoOpForSmallValues(t *testing.T) {
	if err := EnsureWorkerRLimit(512); err != nil {
		t.Errorf("small worker_connections must not touch nginx.conf: %v", err)
	}
}
