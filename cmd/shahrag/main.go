// Command shahrag is the single entry point for the Shahrag panel.
//
// Usage:
//
//	shahrag              Start the interactive CLI menu
//	shahrag serve        Start the web server (used by systemd)
//	shahrag status       Show service status summary
//	shahrag generate     Generate nginx config and reload
//	shahrag version      Print version
//	shahrag -h           Show help
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"shahrag/internal/cli"
	"shahrag/internal/config"
	"shahrag/internal/installer"
	nginxpkg "shahrag/internal/nginx"
	"shahrag/internal/stats"
	"shahrag/internal/web"
)

const version = "1.0.0"

// buildTag marks this specific build. `shahrag version` prints it so you can
// tell at a glance whether the NEW binary is really installed (older builds
// print only "Shahrag v1.0.0" without a tag).
const buildTag = "r26"

// init sets the web layer's build tag before ANY request can be served.
// Assigning it inside runServer was too late for anything that reads it at
// package-init time, and easy to forget on a new code path.
func init() { web.BuildTag = buildTag }

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[shahrag] ")

	// Sub-command routing. "serve" is what systemd calls; everything else
	// falls through to the interactive CLI so that plain `shahrag` opens
	// the menu without extra arguments.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "serve":
			runServer(os.Args[2:])
			return
		case "status":
			os.Exit(cli.RunStatus())
		case "generate", "reload":
			os.Exit(cli.RunGenerate())
		case "menu", "cli", "-i", "--interactive":
			os.Exit(cli.RunMenu())
		case "version", "-v", "--version":
			fmt.Printf("Shahrag v%s (build %s)\n", version, buildTag)
			return
		case "route":
			os.Exit(cli.RunRoute(os.Args[2:]))
		case "boot-guard":
			os.Exit(cli.RunBootGuard())
		case "doctor":
			os.Exit(cli.RunDoctor())
		case "selftest", "test":
			os.Exit(cli.RunSelfTest())
		case "restore":
			restorePath := ""
			if len(os.Args) >= 3 {
				restorePath = os.Args[2]
			}
			os.Exit(cli.RunRestore(restorePath))
		case "init-config":
			// Create the default config file if missing and print its path.
			// Used by install.sh instead of briefly running a server.
			cfg := config.New()
			if _, err := cfg.Read(); err != nil {
				log.Fatalf("cannot initialise config: %v", err)
			}
			fmt.Println(config.ConfigPath)
			return
		case "-h", "--help", "help":
			printHelp()
			return
		}
	}

	// No recognised sub-command → interactive menu (the desired default).
	os.Exit(cli.RunMenu())
}

func printHelp() {
	fmt.Println(`Shahrag — nginx control panel

Usage:
  shahrag              Open interactive menu
  shahrag serve        Start web server (used by systemd)
  shahrag status       Show status
  shahrag generate     Generate nginx config and reload
  shahrag doctor       Print a full diagnostic report
  shahrag boot-guard   Make nginx survive reboots (systemd drop-in + enable)
  shahrag route DOMAIN Show how a domain is routed (rule, DNS, live TLS test)
  shahrag selftest     Test every service end-to-end on the server
  shahrag restore FILE Restore a config backup and regenerate nginx
  shahrag version      Show version
  shahrag -h           Show this help`)
}

func runServer(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", envOr("SHAHRAG_HOST", "0.0.0.0"), "Bind host")
	// 0 means "resolve from config": the panel's configured LocalPort is
	// used, falling back to 8080. This keeps the systemd unit free of
	// hardcoded ports — changing the panel port in the UI/CLI only needs a
	// service restart, and the two can never drift apart.
	port := fs.Int("port", envOrInt("SHAHRAG_PORT", 0), "Listen port (0 = use configured panel port)")
	_ = fs.Parse(args)

	cfg := config.New()
	gen := nginxpkg.NewGenerator(cfg)
	inst := installer.New(cfg)
	collector := stats.NewCollector()
	_ = nginxpkg.EnableStubStatus()

	// ── Boot resilience ─────────────────────────────────────────────
	// After a server reboot nginx was found "inactive" while `nginx -t`
	// reported a valid config: Debian's nginx.service has no Restart=, so a
	// single transient failure at boot (a Reality port still held by
	// xray/x-ui, IPv6 not configured yet for `listen [::]:6038`, a cert on a
	// not-yet-mounted filesystem) leaves the server permanently down.
	// Shahrag installs a systemd drop-in (Restart=on-failure,
	// After=network-online.target), makes sure nginx is enabled, raises
	// worker_rlimit_nofile to match worker_connections, and starts nginx
	// when it is down with a valid config. A watchdog repeats that check
	// every 30s for the lifetime of the panel.
	if c, err := cfg.Read(); err == nil && c.NginxSettings.WorkerConnections > 0 {
		if err := nginxpkg.EnsureWorkerRLimit(c.NginxSettings.WorkerConnections); err != nil {
			log.Printf("worker_rlimit_nofile: %v", err)
		}
	}
	for _, a := range nginxpkg.BootGuard(gen) {
		log.Printf("boot guard: %s", a)
	}
	go nginxpkg.Watchdog(gen, 30*time.Second, log.Printf)

	resolved := resolvePort(cfg, *port)

	srv := web.NewServer(cfg, gen, inst, collector, resolved)

	// Self-healing bind. The configured listen socket may be taken:
	//   • another process holds the port on a specific interface (e.g. a
	//     VPN/cloud-metadata listener) → binding the wildcard fails while
	//     loopback would work;
	//   • or the port is fully busy (e.g. another panel).
	// Instead of crash-looping, fall back to loopback (the panel is always
	// reachable through nginx at 127.0.0.1:<port>) and, as a last resort,
	// to a free port — persisting it in the config so the nginx generator
	// keeps proxying to the right place.
	addr, ln := bindPanel(*host, resolved, cfg)

	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("Shahrag v%s web on http://%s", version, addr)
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

// bindPanel acquires the panel's listen socket, falling back as described
// above. It returns the final address string and the open listener.
func bindPanel(host string, port int, cfg *config.Manager) (string, net.Listener) {
	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return addr, ln
	}
	log.Printf("cannot bind %s: %v", addr, err)

	// Fallback 1: loopback on the same port. The nginx config always
	// proxies to 127.0.0.1:<port>, so the panel keeps working.
	if host != "127.0.0.1" && port > 0 {
		lb := fmt.Sprintf("127.0.0.1:%d", port)
		if ln2, err2 := net.Listen("tcp", lb); err2 == nil {
			log.Printf("bound %s instead (panel reachable via nginx)", lb)
			return lb, ln2
		}
		log.Printf("cannot bind %s either: %v", lb, err)
	}

	// Fallback 2: a free port, persisted into the config so the nginx
	// generator stays consistent.
	free := installer.FindFreePort(port)
	if free > 0 {
		if _, merr := cfg.Mutate(func(c *config.Config) error {
			// Only update when nobody else changed it meanwhile.
			if c.Shahrag.Panel.LocalPort == port || c.Shahrag.Panel.LocalPort == 0 {
				c.Shahrag.Panel.LocalPort = free
				if svc, ok := c.Services[c.Shahrag.Panel.ServiceName]; ok {
					svc.LocalPort = free
					c.Services[c.Shahrag.Panel.ServiceName] = svc
				}
			}
			return nil
		}); merr != nil {
			log.Printf("could not persist new port: %v", merr)
		} else {
			addr2 := fmt.Sprintf("127.0.0.1:%d", free)
			if ln2, err2 := net.Listen("tcp", addr2); err2 == nil {
				log.Printf("configured port %d was busy — panel moved to %s (config updated)", port, addr2)
				return addr2, ln2
			}
		}
	}

	log.Fatalf("no usable listen address for the panel (tried %s and loopback/free-port fallbacks)", addr)
	return "", nil
}

// resolvePort turns the CLI/env port into the effective listen port:
// explicit flag value wins; 0 means "read the panel's configured LocalPort,
// then fall back to 8080".
func resolvePort(cfg *config.Manager, flagPort int) int {
	if flagPort > 0 {
		return flagPort
	}
	if c, err := cfg.Read(); err == nil && c.Shahrag.Panel.LocalPort > 0 {
		return c.Shahrag.Panel.LocalPort
	}
	return 8080
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envOrInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return d
}
