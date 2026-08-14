package web

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handlePanelInfo(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	uptime := float64(0)
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if u, err := strconv.ParseFloat(fields[0], 64); err == nil {
				uptime = u
			}
		}
	}
	hostname, _ := os.Hostname()
	writeJSON(w, 200, map[string]interface{}{
		"app_name":    "Shahrag",
		"app_name_fa": "شاه‌رگ",
		"version":     "1.0.0",
		"installed":   c.Shahrag.Panel.Installed,
		"panel":       c.Shahrag.Panel,
		"system": map[string]interface{}{
			"hostname": hostname,
			"os":       runtime.GOOS,
			"arch":     runtime.GOARCH,
			"go":       runtime.Version(),
			"uptime":   uptime,
			"time":     time.Now().Unix(),
		},
	})
}
