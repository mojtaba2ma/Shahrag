package web

import (
	"net/http"
	"strconv"

	"shahrag/internal/config"
)

func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.stats.Summary())
}

func (s *Server) handleStatsRequests(w http.ResponseWriter, r *http.Request) {
	minutes := atoiDefault(r.URL.Query().Get("minutes"), 60)
	bucket := atoiDefault(r.URL.Query().Get("bucket"), 60)
	_ = bucket
	writeJSON(w, 200, s.stats.RequestsTimeseries(minutes, bucket))
}

func (s *Server) handleStatsConnections(w http.ResponseWriter, r *http.Request) {
	minutes := atoiDefault(r.URL.Query().Get("minutes"), 60)
	writeJSON(w, 200, s.stats.ConnectionsTimeseries(minutes))
}

func (s *Server) handleStatsTopIPs(w http.ResponseWriter, r *http.Request) {
	minutes := atoiDefault(r.URL.Query().Get("minutes"), 60)
	limit := atoiDefault(r.URL.Query().Get("limit"), 10)
	writeJSON(w, 200, s.stats.TopIPs(minutes, limit))
}

func (s *Server) handleStatsTopPaths(w http.ResponseWriter, r *http.Request) {
	minutes := atoiDefault(r.URL.Query().Get("minutes"), 60)
	limit := atoiDefault(r.URL.Query().Get("limit"), 10)
	writeJSON(w, 200, s.stats.TopPaths(minutes, limit))
}

func (s *Server) handleStatsStatus(w http.ResponseWriter, r *http.Request) {
	minutes := atoiDefault(r.URL.Query().Get("minutes"), 60)
	writeJSON(w, 200, s.stats.StatusDistribution(minutes))
}

func (s *Server) handleStatsProto(w http.ResponseWriter, r *http.Request) {
	minutes := atoiDefault(r.URL.Query().Get("minutes"), 60)
	writeJSON(w, 200, s.stats.ProtoTimeseries(minutes))
}

func (s *Server) handleStatsResources(w http.ResponseWriter, r *http.Request) {
	minutes := atoiDefault(r.URL.Query().Get("minutes"), 60)
	writeJSON(w, 200, map[string]interface{}{
		"resources": s.stats.ResourceTimeseries(minutes),
	})
}

func (s *Server) handleStatsRefresh(w http.ResponseWriter, r *http.Request) {
	// Trigger an immediate parse + snapshot by hitting the collector's loop indirectly.
	// The collector already runs in background; we just return current summary.
	writeJSON(w, 200, s.stats.Summary())
}

type topologyDomain struct {
	Name    string `json:"name"`
	Cert    string `json:"cert"`
	Key     string `json:"key"`
	HasCert bool   `json:"has_cert"`
}
type topologyBinding struct {
	Domain    string `json:"domain"`
	Subdomain string `json:"subdomain"`
	FQDN      string `json:"fqdn"`
	Cert      string `json:"cert"`
	HasCert   bool   `json:"has_cert"`
}
type topologyService struct {
	Name       string            `json:"name"`
	LocalPort  int               `json:"local_port"`
	ListenPort int               `json:"listen_port"`
	Path       string            `json:"path"`
	PathOwned  bool              `json:"path_owned"`
	SSLBackend bool              `json:"ssl_backend"`
	Bindings   []topologyBinding `json:"bindings"`
	IsPanel    bool              `json:"is_panel"`
}
type topologyReality struct {
	Name      string `json:"name"`
	SNI       string `json:"sni"`
	LocalPort int    `json:"local_port"`
	Ports     []int  `json:"ports"`
}
type topologyResponse struct {
	Domains         []topologyDomain  `json:"domains"`
	Services        []topologyService `json:"services"`
	RealityServices []topologyReality `json:"reality_services"`
	ListenPorts     []int             `json:"listen_ports"`
	RealityEnabled  bool              `json:"reality_enabled"`
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	panelName := c.Shahrag.Panel.ServiceName
	resp := topologyResponse{
		ListenPorts:    c.ListenPorts,
		RealityEnabled: c.Reality.Enabled,
	}
	for name, d := range c.Domains {
		resp.Domains = append(resp.Domains, topologyDomain{
			Name: name, Cert: d.Cert, Key: d.Key, HasCert: d.Cert != "",
		})
	}
	for name, svc := range c.Services {
		var binds []topologyBinding
		for _, b := range svc.Bindings {
			fqdn := b.Domain
			if b.Subdomain != "" {
				fqdn = b.Subdomain + "." + b.Domain
			}
			d := c.Domains[b.Domain]
			binds = append(binds, topologyBinding{
				Domain: b.Domain, Subdomain: b.Subdomain, FQDN: fqdn,
				Cert: d.Cert, HasCert: d.Cert != "",
			})
		}
		resp.Services = append(resp.Services, topologyService{
			Name: name, LocalPort: svc.LocalPort, ListenPort: svc.ListenPort,
			Path: svc.Path, PathOwned: svc.PathOwned, SSLBackend: svc.SSLBackend,
			Bindings: binds, IsPanel: name == panelName,
		})
	}
	for name, rsvc := range c.Reality.Services {
		resp.RealityServices = append(resp.RealityServices, topologyReality{
			Name: name, SNI: rsvc.SNI, LocalPort: rsvc.LocalPort, Ports: rsvc.Ports,
		})
	}
	writeJSON(w, 200, resp)
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

var _ = config.Config{}
