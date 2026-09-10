package web

import (
	"net/http"
	"sort"
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

// handleStatsBans returns the ban series and its headline figures.
//
// Two numbers that must not be confused: ACTIVE is a gauge that rises and
// falls as bans expire, TOTAL is a counter whose SLOPE answers "am I under
// attack?". A chart of the gauge alone makes a ban wave invisible an hour
// after it ended.
func (s *Server) handleStatsBans(w http.ResponseWriter, r *http.Request) {
	minutes := atoiDefault(r.URL.Query().Get("minutes"), 60)
	writeJSON(w, 200, map[string]interface{}{
		"series":  s.stats.BanTimeseries(minutes),
		"summary": s.stats.BanSummaryNow(),
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

	// Added for the topology map. Without these the map could draw the
	// shape of the routing but not its state, and a map that shows a
	// disabled service exactly like a live one is worse than no map.
	Disabled bool `json:"disabled"`
	// Target is where traffic actually goes: 127.0.0.1, another host, or
	// $ssl_preread_server_name for a passthrough.
	Target string `json:"target"`
	// Passthrough marks a service whose traffic is forwarded opaquely.
	Passthrough bool `json:"passthrough"`
	// IPLocked marks a service restricted to particular addresses, and
	// Gated one behind the bot shield. Both change who can reach it, so
	// both belong on a picture of who can reach what.
	IPLocked bool `json:"ip_locked"`
	Gated    bool `json:"gated"`
}
type topologyReality struct {
	Name      string `json:"name"`
	SNI       string `json:"sni"`
	LocalPort int    `json:"local_port"`
	Ports     []int  `json:"ports"`

	Disabled    bool   `json:"disabled"`
	Target      string `json:"target"`
	Passthrough bool   `json:"passthrough"`
}

// topologyPort is one port the server listens on, and what happens to a
// connection that arrives there.
//
// This is the piece the map needed that did not exist: the config stores
// listen ports and services separately, and working out "port 443 is
// SNI-split, port 8443 is plain HTTPS" required knowing rules that live in
// the generator. Deriving it once here means the map, the CLI and the bot
// all describe the routing identically.
type topologyPort struct {
	Port int `json:"port"`
	// Kind is how a connection arriving here is dispatched:
	//   "sni"   - the stream module reads the TLS SNI and splits on it
	//   "https" - an ordinary TLS server block, split by Host and path
	//   "http"  - plain HTTP, normally only the ACME/redirect port
	Kind string `json:"kind"`
	// Services names what is reachable through this port.
	Services []string `json:"services"`
	// SNINames are the hostnames split off before nginx's http block, only
	// meaningful when Kind is "sni".
	SNINames []string `json:"sni_names,omitempty"`
	// Fallback is where a connection that matches no SNI name ends up.
	Fallback int `json:"fallback,omitempty"`
}

type topologyResponse struct {
	Domains         []topologyDomain  `json:"domains"`
	Services        []topologyService `json:"services"`
	RealityServices []topologyReality `json:"reality_services"`
	ListenPorts     []int             `json:"listen_ports"`
	RealityEnabled  bool              `json:"reality_enabled"`

	// Ports is the derived view described above.
	Ports []topologyPort `json:"ports"`
	// RealityHTTPPort is where the stream module sends anything that did
	// not match an SNI rule.
	RealityHTTPPort int `json:"reality_http_port"`
	// PanelName lets the map mark the panel's own service, which is the
	// one an operator must not accidentally break.
	PanelName string `json:"panel_name"`
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
			Disabled:    svc.Disabled,
			Target:      svc.Target,
			Passthrough: svc.Target == config.PassthroughTarget,
			IPLocked:    svc.IPRestricted(),
			Gated:       svc.GateEnabled(),
		})
	}
	for name, rsvc := range c.Reality.Services {
		resp.RealityServices = append(resp.RealityServices, topologyReality{
			Name: name, SNI: rsvc.SNI, LocalPort: rsvc.LocalPort, Ports: rsvc.Ports,
			Disabled:    rsvc.Disabled,
			Target:      rsvc.Target,
			Passthrough: rsvc.Target == config.PassthroughTarget,
		})
	}

	// Sorted so the map is stable between refreshes. Go randomises map
	// iteration, and a picture whose boxes move every five seconds is
	// unreadable.
	sort.Slice(resp.Domains, func(i, j int) bool { return resp.Domains[i].Name < resp.Domains[j].Name })
	sort.Slice(resp.Services, func(i, j int) bool { return resp.Services[i].Name < resp.Services[j].Name })
	sort.Slice(resp.RealityServices, func(i, j int) bool {
		return resp.RealityServices[i].Name < resp.RealityServices[j].Name
	})

	resp.PanelName = panelName
	resp.RealityHTTPPort = c.Reality.HTTPPort
	resp.Ports = derivePorts(c, resp)
	writeJSON(w, 200, resp)
}

// derivePorts works out what actually happens on each listening port.
//
// The config stores ports and services separately, so "which services can I
// reach on 443, and is it SNI-split or plain HTTPS?" is a question nobody
// could answer without knowing the generator's rules. Answering it once,
// here, is what lets the map, the CLI status view and the Telegram bot all
// describe the routing in the same words.
func derivePorts(c *config.Config, resp topologyResponse) []topologyPort {
	// SNI-split ports come from the reality section: the stream module
	// binds them and dispatches on the TLS server name.
	sniPorts := map[int]*topologyPort{}
	if c.Reality.Enabled {
		for _, rs := range resp.RealityServices {
			if rs.Disabled {
				continue
			}
			for _, p := range rs.Ports {
				tp := sniPorts[p]
				if tp == nil {
					tp = &topologyPort{Port: p, Kind: "sni", Fallback: c.Reality.HTTPPort}
					sniPorts[p] = tp
				}
				tp.Services = append(tp.Services, rs.Name)
				if rs.SNI != "" {
					tp.SNINames = append(tp.SNINames, rs.SNI)
				}
			}
		}
	}

	// Everything else is an ordinary http/https server block.
	httpPorts := map[int]*topologyPort{}
	touch := func(port int, kind string) *topologyPort {
		if port <= 0 {
			return nil
		}
		if tp := sniPorts[port]; tp != nil {
			// A port that the stream module owns is not also an http
			// listener; the fallback port is where its traffic lands.
			return nil
		}
		tp := httpPorts[port]
		if tp == nil {
			tp = &topologyPort{Port: port, Kind: kind}
			httpPorts[port] = tp
		}
		return tp
	}
	for _, p := range c.ListenPorts {
		touch(p, "https")
	}
	for _, svc := range resp.Services {
		if svc.Disabled {
			continue
		}
		port := svc.ListenPort
		if port <= 0 {
			port = 443
		}
		if tp := touch(port, "https"); tp != nil {
			tp.Services = append(tp.Services, svc.Name)
		}
	}
	// The reality fallback port is a plain HTTP listener behind the
	// stream module, not something a client connects to directly.
	if c.Reality.Enabled && c.Reality.HTTPPort > 0 {
		if tp := touch(c.Reality.HTTPPort, "http"); tp != nil {
			tp.Kind = "http"
		}
	}

	out := make([]topologyPort, 0, len(sniPorts)+len(httpPorts))
	for _, tp := range sniPorts {
		sort.Strings(tp.Services)
		sort.Strings(tp.SNINames)
		out = append(out, *tp)
	}
	for _, tp := range httpPorts {
		sort.Strings(tp.Services)
		out = append(out, *tp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
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
