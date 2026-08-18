package cli

// `shahrag boot-guard` — make nginx survive a reboot.
//
// This exists because after a server reboot nginx was inactive while its
// configuration was perfectly valid. See internal/nginx/bootguard.go for the
// full explanation of the causes.

import (
	"fmt"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
	"shahrag/internal/systemd"
)

// RunBootGuard applies every boot-safety measure and prints what it did.
func RunBootGuard() int {
	fmt.Println("Shahrag — nginx boot guard")
	fmt.Println("════════════════════════════════════════════")

	cfg := config.New()
	gen := nginxpkg.NewGenerator(cfg)

	if c, err := cfg.Read(); err == nil && c.NginxSettings.WorkerConnections > 0 {
		if err := nginxpkg.EnsureWorkerRLimit(c.NginxSettings.WorkerConnections); err != nil {
			fmt.Printf("  worker_rlimit_nofile: %v\n", err)
		} else if n := nginxpkg.WorkerRLimit(); n > 0 {
			fmt.Printf("  worker_rlimit_nofile: %d (matches worker_connections)\n", n)
		}
	}

	actions := nginxpkg.BootGuard(gen)
	if len(actions) == 0 {
		fmt.Println("  everything already in place — nothing to change")
	}
	for _, a := range actions {
		fmt.Printf("  • %s\n", a)
	}

	fmt.Println("──── result ─────────────────────────────────")
	enabledTxt := red("NO — it will not start after a reboot")
	if nginxpkg.IsEnabled() {
		enabledTxt = green("yes")
	}
	fmt.Printf("  nginx enabled at boot: %s\n", enabledTxt)
	fmt.Printf("  systemd drop-in:       %s\n", nginxpkg.SystemdDropInSummary())
	fmt.Printf("  nginx running:         %s\n", yn(systemd.IsActive("nginx")))
	if !systemd.IsActive("nginx") {
		if reason := nginxpkg.LastFailureReason(); reason != "" {
			fmt.Println("  why it is not running:")
			fmt.Printf("    %s\n", reason)
		}
	}
	return 0
}
