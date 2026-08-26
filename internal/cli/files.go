package cli

// Viewing the raw configuration files from the terminal menu.
//
// `shahrag doctor` already dumps config.json, but the generated nginx files
// are what nginx actually serves — and when the two disagree, seeing both is
// the fastest way to understand why. This puts them one keystroke away
// instead of requiring the operator to remember four paths.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

type viewableFile struct {
	label     string
	path      string
	generated bool
}

// viewableFiles resolves the paths from the live config, so a custom output
// location is shown rather than the default.
func viewableFiles(cfg *config.Manager) []viewableFile {
	gateway := nginxpkg.GatewayPath()
	stream := nginxpkg.StreamPath()
	if c, err := cfg.Read(); err == nil {
		if c.Nginx.OutputPath != "" {
			gateway = c.Nginx.OutputPath
		}
		if c.Nginx.StreamOutputPath != "" {
			stream = c.Nginx.StreamOutputPath
		}
	}
	return []viewableFile{
		{"config.json (panel configuration)", config.ConfigPath, false},
		{"gateway.conf (generated HTTP)", gateway, true},
		{"stream-gateway.conf (generated SNI routing)", stream, true},
		{"nginx.conf (main nginx configuration)", nginxpkg.DefaultNginxConf, false},
	}
}

// menuFiles lists the configuration files and prints the chosen one.
func menuFiles(cfg *config.Manager, in *bufio.Reader) {
	for {
		list := viewableFiles(cfg)
		fmt.Println("\n── Config files ──")
		for i, f := range list {
			status := ""
			if st, err := os.Stat(f.path); err == nil {
				status = fmt.Sprintf("%.1f KB, %s", float64(st.Size())/1024,
					st.ModTime().Format("2006-01-02 15:04"))
			} else {
				status = red("missing")
			}
			gen := ""
			if f.generated {
				gen = yellow(" [generated]")
			}
			fmt.Printf("  %d) %s%s\n     %s  (%s)\n", i+1, f.label, gen, f.path, status)
		}
		fmt.Println("  0) Back")
		fmt.Print("\nChoose: ")
		choice := strings.TrimSpace(mustRead(in))
		if choice == "0" || choice == "" {
			return
		}
		idx := -1
		for i := range list {
			if choice == fmt.Sprint(i+1) {
				idx = i
			}
		}
		if idx < 0 {
			continue
		}
		showFile(list[idx], in)
	}
}

func showFile(f viewableFile, in *bufio.Reader) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		fmt.Println(red("cannot read " + f.path + ": " + err.Error()))
		pause(in)
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	fmt.Printf("\n── %s ──\n", f.path)
	fmt.Printf("   %d lines, %.1f KB", len(lines), float64(len(data))/1024)
	if f.generated {
		fmt.Print(yellow("   [generated — 'Generate' rewrites this file]"))
	}
	fmt.Println()

	// A 400-line gateway.conf scrolled straight past the top of the screen,
	// so long files are paged instead of dumped.
	const page = 60
	if len(lines) <= page {
		fmt.Println(strings.Repeat("─", 60))
		fmt.Println(strings.Join(lines, "\n"))
		fmt.Println(strings.Repeat("─", 60))
		pause(in)
		return
	}
	for start := 0; start < len(lines); start += page {
		end := start + page
		if end > len(lines) {
			end = len(lines)
		}
		fmt.Println(strings.Repeat("─", 60))
		for i := start; i < end; i++ {
			fmt.Printf("%4d │ %s\n", i+1, lines[i])
		}
		fmt.Println(strings.Repeat("─", 60))
		if end >= len(lines) {
			break
		}
		fmt.Printf("lines %d–%d of %d — Enter for more, 's' to stop, 'w' to save a copy: ",
			start+1, end, len(lines))
		ans := strings.ToLower(strings.TrimSpace(mustRead(in)))
		if ans == "s" || ans == "q" {
			return
		}
		if ans == "w" {
			saveCopy(f, data)
			return
		}
	}
	fmt.Print("End of file. Press 'w' to save a copy, or Enter to go back: ")
	if strings.ToLower(strings.TrimSpace(mustRead(in))) == "w" {
		saveCopy(f, data)
	}
}

// nowStampCLI is the timestamp used for saved copies.
func nowStampCLI() string { return time.Now().Format("20060102-150405") }

func saveCopy(f viewableFile, data []byte) {
	dir := filepath.Join("/var/backups/shahrag", "views")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Println(red(err.Error()))
		return
	}
	name := filepath.Join(dir, fmt.Sprintf("%s-%s", nowStampCLI(), filepath.Base(f.path)))
	if err := os.WriteFile(name, data, 0o600); err != nil {
		fmt.Println(red(err.Error()))
		return
	}
	fmt.Println(green("Saved a copy to " + name))
}
