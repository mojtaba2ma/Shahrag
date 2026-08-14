// Command shahrag is the single entry point for the Shahrag panel.
//
// Usage:
//
//	shahrag              Start the interactive CLI menu
//	shahrag serve        Start the web server (used by systemd)
//	shahrag status       Show service status summary
//	shahrag generate     Generate nginx config and reload
//	shahrag -h           Show help
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[shahrag] ")

	// Sub-command routing. "serve" is what systemd calls; everything else
	// falls through to the interactive CLI so that plain `shahrag` opens
	// the menu without extra arguments.
	if len(os.Args) >= 2 {
		switch os.Args[0:1][0] {
		case "":
		}
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
  shahrag -h           Show this help`)
}

func runServer(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", envOr("SHAHRAG_HOST", "0.0.0.0"), "Bind host")
	port := fs.Int("port", envOrInt("SHAHRAG_PORT", 8080), "Listen port")
	_ = fs.Parse(args)

	cfg := config.New()
	gen := nginxpkg.NewGenerator(cfg)
	inst := installer.New(cfg)
	collector := stats.NewCollector()
	_ = nginxpkg.EnableStubStatus()

	srv := web.NewServer(cfg, gen, inst, collector)
	addr := fmt.Sprintf("%s:%d", *host, *port)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("Shahrag v1.0 web on http://%s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envOrInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return d
}
