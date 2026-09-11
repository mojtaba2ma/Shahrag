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
	"runtime/debug"
	"syscall"
	"time"

	"shahrag/internal/backup"
	"shahrag/internal/banner"
	"shahrag/internal/cli"
	"shahrag/internal/config"
	"shahrag/internal/health"
	"shahrag/internal/installer"
	nginxpkg "shahrag/internal/nginx"
	"shahrag/internal/stats"
	"shahrag/internal/web"
)

const version = "1.0.0"

// buildTag marks this specific build. `shahrag version` prints it so you can
// tell at a glance whether the NEW binary is really installed (older builds
// print only "Shahrag v1.0.0" without a tag).
const buildTag = "r51"

// init sets the web layer's build tag before ANY request can be served.
// Assigning it inside runServer was too late for anything that reads it at
// package-init time, and easy to forget on a new code path.
func init() {
	web.BuildTag = buildTag
	cli.BuildTag = buildTag
}

// tuneRuntime constrains the Go runtime for a small VPS.
//
// Go's default garbage collector waits until the heap has DOUBLED before
// collecting, and never returns memory on a schedule. That is the right
// trade on a machine with memory to spare and the wrong one on a 1 GB VPS
// running nginx, xray and a DNS resolver beside the panel.
//
// Measured with a distributed scan driving the ban engine hard:
//
//	default            peak RSS 52.7 MB, settling at 39.2 MB
//	limit + GOGC=50    peak RSS 37.0 MB, settling at 37.0 MB
//
// A 30% lower peak matters because the peak is what triggers the OOM
// killer, and on a swap-backed box it is also what gets paged out and then
// has to be faulted back in.
//
// The soft limit is a CEILING, not a reservation: the panel idles at about
// 6.6 MB and only approaches this figure while absorbing an attack. Go
// treats it as advisory and will exceed it rather than deadlock, so there
// is no risk of the panel refusing to work — it simply collects harder as
// it approaches. Both values are overridable through the standard
// environment variables for anyone running on a larger machine.
func tuneRuntime() {
	if os.Getenv("GOMEMLIMIT") == "" {
		// 96 MB leaves generous headroom over the measured worst case
		// while still being a small fraction of a 1 GB server.
		debug.SetMemoryLimit(96 << 20)
	}
	if os.Getenv("GOGC") == "" {
		// Collect at +50% heap growth rather than +100%. The panel's
		// allocation rate is tiny, so the extra collections cost
		// microseconds and are invisible next to the memory they save.
		debug.SetGCPercent(50)
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[shahrag] ")
	tuneRuntime()

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
		case "renew-certs":
			// Entry point for the systemd timer.
			os.Exit(cli.RunRenew())
		case "doctor":
			os.Exit(cli.RunDoctor())
		case "health":
			// The fallback path: when the web panel is unreachable, this
			// is the view an operator needs, and that is exactly the
			// moment the panel cannot show it.
			os.Exit(cli.RunHealth())
		case "map", "routes":
			os.Exit(cli.RunMap())
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
			// A FRESH install gets tuning values computed from the real
			// machine. nginx's own defaults suit no particular machine,
			// and the moment to pick better ones is while we are already
			// looking at the hardware.
			//
			// Only on a fresh install: an existing config is never
			// touched, because an upgrade that silently rewrote somebody's
			// nginx tuning would be indefensible. That is what the
			// Configured flag distinguishes.
			initTuningIfFresh(cfg)
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
  shahrag renew-certs  Renew certificates that are near expiry
  shahrag status       Show status
  shahrag generate     Generate nginx config and reload
  shahrag doctor       Print a full diagnostic report
  shahrag health       Server health summary (works without the web panel)
  shahrag map          What listens where, and where it goes
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

	// ── Automatic banning ───────────────────────────────────────────
	// The engine watches nginx's logs and maintains the ban list; the
	// generator turns that list into a `geo` block. When the list changes
	// the config is regenerated and reloaded, which nginx does without
	// dropping a connection.
	//
	// Wired after NewServer because the callback needs the generator and
	// the engine needs the callback — building both at once would be a
	// cycle.
	bans := banner.New(cfg, nginxpkg.HoneypotLogPath, banner.AccessLogPath, nil)
	bans.SetOnChange(func() {
		if _, err := gen.GenerateAndReload(); err != nil {
			log.Printf("bans: could not apply: %v", err)
		}
	})
	nginxpkg.SetBanProvider(bans)
	srv.SetBanEngine(bans)
	// The statistics collector samples the ban counts on its own loop, so
	// the chart is built from the same engine the panel reads.
	stats.SetBanCounter(bans)
	bans.Start()

	// ── Scheduled backups ────────────────────────────────────
	//
	// Started here rather than lazily on first use, because the whole
	// point is that it runs without anybody asking. The scheduler ticks
	// once a minute and does nothing at all unless a backup is due, so
	// the cost of always starting it is one config read per minute.
	backup.BuildTag = buildTag
	bkEngine := backup.New(cfg)
	bkSender := backup.NewSender(cfg)
	bkSched := backup.NewScheduler(bkEngine, cfg)
	bkSched.SetOffsite(bkSender.Send)
	bkSched.SetNotifier(func(msg string) {
		// Routed through the same bot as the ban alerts. A backup that
		// stops working is exactly the kind of failure nobody notices
		// for months, so it is worth a message.
		if b := srv.Bot(); b != nil {
			b.Notify(msg)
		}
	})
	srv.SetBackup(bkEngine, bkSched, bkSender)
	bkSched.Start()
	defer bkSched.Stop()

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
	// Flush statistics before exiting, so an ordinary restart or upgrade
	// loses at most the last few seconds instead of up to five minutes.
	if bans != nil {
		bans.Stop()
	}
	if collector != nil {
		if err := collector.Save(); err != nil {
			log.Printf("stats: could not save on shutdown: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

// initTuningIfFresh fills the advanced nginx tuning from the real machine,
// but only when it has never been configured.
//
// Called from `init-config`, which install.sh runs once. The guard is the
// Profile field being empty: an upgrade over an existing installation finds
// it set (or finds Enabled false because somebody deliberately turned it
// off) and changes nothing.
//
// It is left DISABLED even on a fresh install. The values are computed and
// stored so the panel shows sensible numbers rather than empty boxes, but
// nothing reaches nginx until an operator looks at the page and turns it on.
// Shipping a config that quietly rewrites nginx's behaviour on first boot is
// how an install becomes something people are afraid to run.
func initTuningIfFresh(cfg *config.Manager) {
	c, err := cfg.Read()
	if err != nil || c == nil {
		return
	}
	if c.NginxSettings.Tuning.Profile != "" || c.NginxSettings.Tuning.Enabled {
		return // already configured; never overwrite
	}

	si := health.ReadSysInfo()
	profile := config.ProfileBalanced
	switch mb := si.RAMBytes / (1 << 20); {
	case mb > 0 && mb <= 1280:
		profile = config.ProfileSmall
	case mb >= 6144:
		profile = config.ProfileBusy
	}

	if _, err := cfg.Mutate(func(c *config.Config) error {
		t := config.ApplyRecommendations(c.NginxSettings.Tuning,
			config.Recommend(si, profile))
		t.Profile = profile
		t.Enabled = false // computed, stored, NOT applied
		c.NginxSettings.Tuning = t
		return nil
	}); err != nil {
		log.Printf("could not pre-fill the nginx tuning: %v", err)
		return
	}
	log.Printf("nginx tuning pre-filled for a %q machine (%d cores, %d MB RAM) — "+
		"review and enable it in Settings → Nginx",
		profile, si.Cores, si.RAMBytes/(1<<20))
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
