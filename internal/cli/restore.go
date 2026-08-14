// Package cli — `shahrag restore <file>`: restore the panel config from a
// backup JSON, regenerate the nginx config and restart the panel service.
// This is the fastest way to undo a bad wizard run.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
	"shahrag/internal/systemd"
)

// RunRestore restores a config backup and regenerates nginx.
// Returns the process exit code.
func RunRestore(path string) int {
	if path == "" {
		fmt.Println("usage: shahrag restore <backup.json>")
		fmt.Println("  e.g. sudo shahrag restore /var/backups/shahrag/wizard-pre-20260814-232714.json")
		return 1
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("cannot read backup: %v\n", err)
		return 1
	}
	var c config.Config
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Printf("backup is not a valid Shahrag config: %v\n", err)
		return 1
	}
	if c.Domains == nil || c.Services == nil {
		fmt.Println("backup is missing 'domains'/'services' — not a Shahrag config")
		return 1
	}

	// Keep a copy of the CURRENT config before overwriting it.
	if cur, err := os.ReadFile(config.ConfigPath); err == nil {
		_ = os.MkdirAll("/var/backups/shahrag", 0o755)
		pre := filepath.Join("/var/backups/shahrag", "pre-restore-"+time.Now().Format("20060102-150405")+".json")
		if err := os.WriteFile(pre, cur, 0o600); err == nil {
			fmt.Printf("current config saved to %s\n", pre)
		}
	}

	cfg := config.New()
	if err := cfg.Write(&c); err != nil {
		fmt.Printf("cannot write restored config: %v\n", err)
		return 1
	}
	fmt.Printf("config restored from %s\n", path)

	gen := nginxpkg.NewGenerator(cfg)
	res, err := gen.GenerateAndReload()
	if err != nil {
		fmt.Printf("nginx generation failed: %v (the previous nginx files were restored)\n", err)
		return 1
	}
	if ok, _ := res["ok"].(bool); !ok {
		if t, ok2 := res["test"].(nginxpkg.TestResult); ok2 {
			fmt.Printf("nginx -t rejected the generated config (previous files restored):\n%s\n", t.Stderr)
		} else {
			fmt.Println("nginx -t rejected the generated config (previous files restored).")
		}
		return 1
	}
	fmt.Println("nginx config regenerated and reloaded.")

	if err := systemd.Restart(systemd.UnitName); err != nil {
		fmt.Printf("warning: could not restart the shahrag service: %v\n", err)
	}
	fmt.Println("Done. Panel state restored.")
	return 0
}
