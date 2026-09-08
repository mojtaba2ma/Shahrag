package web

// The advanced nginx tuning API.
//
// Three endpoints:
//
//	GET  /api/settings/tuning          current values, plus a recommendation
//	                                   for every field and the system facts
//	                                   those recommendations came from
//	PUT  /api/settings/tuning          save and apply
//	POST /api/settings/tuning/measure  measure the uplink, on request only
//
// The recommendations travel WITH the current values in one response rather
// than behind a second endpoint. That is what makes the per-field auto-fill
// button instant: the browser already has the suggested number and the
// reason for it before the operator clicks anything.

import (
	"net/http"
	"strconv"

	"shahrag/internal/config"
	"shahrag/internal/health"
	nginxpkg "shahrag/internal/nginx"
)

type tuningResp struct {
	Tuning config.Tuning `json:"tuning"`
	// Enabled restated without omitempty, for the same reason as
	// everywhere else in this codebase: a stored false must appear in the
	// JSON as false, not vanish.
	Enabled bool `json:"enabled"`

	// Recommended is a suggestion per field, each with a reason code the
	// UI translates.
	Recommended map[string]config.Recommendation `json:"recommended"`

	// System is what those recommendations were computed from. Shown in
	// the UI so the operator can see WHY a number was suggested and judge
	// it, rather than being handed a figure to trust.
	System struct {
		Cores          int    `json:"cores"`
		RAMBytes       int64  `json:"ram_bytes"`
		AvailableBytes int64  `json:"available_bytes"`
		SwapBytes      int64  `json:"swap_bytes"`
		FileMax        int    `json:"file_max"`
		NofileHard     int    `json:"nofile_hard"`
		LinkMbits      int    `json:"link_mbits"`
		LinkSource     string `json:"link_source"`
	} `json:"system"`

	// Profiles the UI can offer as one-click presets.
	Profiles []string `json:"profiles"`
	// Suggested is the profile that fits this machine, so the UI can
	// pre-select it rather than making the operator guess.
	Suggested string `json:"suggested_profile"`

	// Warnings about the CURRENT combination — things that are allowed but
	// worth saying out loud, like "you switched off the log the statistics
	// page reads".
	Warnings []string `json:"warnings,omitempty"`

	// WorkerConnections is carried here too because several
	// recommendations are relative to it and the UI shows the pairing.
	WorkerConnections int `json:"worker_connections"`

	// MainInstalled says whether the main-context block is actually in
	// nginx.conf. Settings can be saved while the edit failed, and the UI
	// must be able to say so rather than implying it took effect.
	MainInstalled bool `json:"main_installed"`
}

func (s *Server) handleGetTuning(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	t := c.NginxSettings.Tuning

	si := health.ReadSysInfo()
	// A measured link speed, if one was taken, beats whatever the driver
	// claims — see the comment on readLinkSpeed for why the driver's
	// figure is so often wrong on a VPS.
	linkSource := "kernel"
	if t.Enabled && measuredLinkMbits > 0 {
		si.LinkMbits = measuredLinkMbits
		linkSource = "measured"
	} else if measuredLinkMbits > 0 {
		si.LinkMbits = measuredLinkMbits
		linkSource = "measured"
	}
	if si.LinkMbits == 0 {
		linkSource = "unknown"
	}

	profile := t.Profile
	if profile == "" {
		profile = suggestProfile(si)
	}

	resp := tuningResp{
		Tuning:            t,
		Enabled:           t.Enabled,
		Recommended:       config.Recommend(si, profile),
		Profiles:          []string{config.ProfileSmall, config.ProfileBalanced, config.ProfileBusy},
		Suggested:         suggestProfile(si),
		WorkerConnections: c.NginxSettings.WorkerConnections,
		Warnings:          config.TuningWarnings(t, c.NginxSettings.WorkerConnections),
		MainInstalled:     nginxpkg.MainTuningInstalled(),
	}
	resp.System.Cores = si.Cores
	resp.System.RAMBytes = si.RAMBytes
	resp.System.AvailableBytes = si.AvailableBytes
	resp.System.SwapBytes = si.SwapBytes
	resp.System.FileMax = si.FileMax
	resp.System.NofileHard = si.NofileHard
	resp.System.LinkMbits = si.LinkMbits
	resp.System.LinkSource = linkSource

	writeJSON(w, 200, resp)
}

// suggestProfile picks a preset from what the machine is.
//
// The thresholds are deliberately about MEMORY rather than cores: on the
// servers this panel runs on, memory is what runs out first — a 1 GB box
// with 2 cores is constrained by the gigabyte, not by the cores.
func suggestProfile(si config.SysInfo) string {
	mb := si.RAMBytes / (1 << 20)
	switch {
	case mb > 0 && mb <= 1280:
		return config.ProfileSmall
	case mb >= 6144:
		return config.ProfileBusy
	default:
		return config.ProfileBalanced
	}
}

type tuningReq struct {
	// Profile, when set to one of the presets, fills every field from that
	// profile's recommendations BEFORE the explicit fields below are
	// applied. That ordering is what lets the UI send "apply the small
	// profile, but I want a 60 MB body limit" in one request.
	Profile *string `json:"profile"`
	// AutoFill asks the server to compute every field from the live system
	// facts. Same ordering rule: explicit fields still win.
	AutoFill bool `json:"auto_fill"`

	Enabled *bool `json:"enabled"`

	WorkerProcesses    *string `json:"worker_processes"`
	WorkerRLimitNofile *int    `json:"worker_rlimit_nofile"`
	MultiAccept        *bool   `json:"multi_accept"`

	KeepaliveTimeout    *int `json:"keepalive_timeout"`
	KeepaliveRequests   *int `json:"keepalive_requests"`
	ClientHeaderTimeout *int `json:"client_header_timeout"`
	ClientBodyTimeout   *int `json:"client_body_timeout"`
	SendTimeout         *int `json:"send_timeout"`

	ProxyConnectTimeout  *int  `json:"proxy_connect_timeout"`
	ProxyReadTimeout     *int  `json:"proxy_read_timeout"`
	ProxySendTimeout     *int  `json:"proxy_send_timeout"`
	ProxySocketKeepalive *bool `json:"proxy_socket_keepalive"`
	UpstreamKeepalive    *int  `json:"upstream_keepalive"`
	ProxyBufferingOff    *bool `json:"proxy_buffering_off"`

	ClientMaxBodyMB     *int `json:"client_max_body_mb"`
	ClientBodyBufferKB  *int `json:"client_body_buffer_kb"`
	LargeClientHeaderKB *int `json:"large_client_header_kb"`

	GzipEnabled   *bool `json:"gzip_enabled"`
	GzipCompLevel *int  `json:"gzip_comp_level"`
	GzipMinLength *int  `json:"gzip_min_length"`

	SSLSessionCacheMB    *int    `json:"ssl_session_cache_mb"`
	SSLSessionTimeoutMin *int    `json:"ssl_session_timeout_min"`
	SSLECDHCurve         *string `json:"ssl_ecdh_curve"`

	ServerTokensOff  *bool `json:"server_tokens_off"`
	LimitConnPerIP   *int  `json:"limit_conn_per_ip"`
	OpenFileCacheMax *int  `json:"open_file_cache_max"`
	AccessLogOff     *bool `json:"access_log_off"`
}

func (s *Server) handleSetTuning(w http.ResponseWriter, r *http.Request) {
	var body tuningReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}

	si := health.ReadSysInfo()
	if measuredLinkMbits > 0 {
		si.LinkMbits = measuredLinkMbits
	}

	_, err := s.cfg.Mutate(func(c *config.Config) error {
		t := c.NginxSettings.Tuning

		// Presets and auto-fill run first, so an explicit field in the
		// same request always wins over the value they computed.
		if body.Profile != nil && *body.Profile != "" && *body.Profile != config.ProfileCustom {
			t = config.ApplyRecommendations(t, config.Recommend(si, *body.Profile))
			t.Profile = *body.Profile
		}
		if body.AutoFill {
			p := t.Profile
			if p == "" {
				p = suggestProfile(si)
			}
			t = config.ApplyRecommendations(t, config.Recommend(si, p))
			t.Profile = p
		}

		set := func(dst *int, src *int) {
			if src != nil {
				*dst = *src
			}
		}
		setB := func(dst *bool, src *bool) {
			if src != nil {
				*dst = *src
			}
		}
		setS := func(dst *string, src *string) {
			if src != nil {
				*dst = *src
			}
		}

		setB(&t.Enabled, body.Enabled)
		setS(&t.WorkerProcesses, body.WorkerProcesses)
		set(&t.WorkerRLimitNofile, body.WorkerRLimitNofile)
		setB(&t.MultiAccept, body.MultiAccept)

		set(&t.KeepaliveTimeout, body.KeepaliveTimeout)
		set(&t.KeepaliveRequests, body.KeepaliveRequests)
		set(&t.ClientHeaderTimeout, body.ClientHeaderTimeout)
		set(&t.ClientBodyTimeout, body.ClientBodyTimeout)
		set(&t.SendTimeout, body.SendTimeout)

		set(&t.ProxyConnectTimeout, body.ProxyConnectTimeout)
		set(&t.ProxyReadTimeout, body.ProxyReadTimeout)
		set(&t.ProxySendTimeout, body.ProxySendTimeout)
		setB(&t.ProxySocketKeepalive, body.ProxySocketKeepalive)
		set(&t.UpstreamKeepalive, body.UpstreamKeepalive)
		setB(&t.ProxyBufferingOff, body.ProxyBufferingOff)

		set(&t.ClientMaxBodyMB, body.ClientMaxBodyMB)
		set(&t.ClientBodyBufferKB, body.ClientBodyBufferKB)
		set(&t.LargeClientHeaderKB, body.LargeClientHeaderKB)

		setB(&t.GzipEnabled, body.GzipEnabled)
		set(&t.GzipCompLevel, body.GzipCompLevel)
		set(&t.GzipMinLength, body.GzipMinLength)

		set(&t.SSLSessionCacheMB, body.SSLSessionCacheMB)
		set(&t.SSLSessionTimeoutMin, body.SSLSessionTimeoutMin)
		setS(&t.SSLECDHCurve, body.SSLECDHCurve)

		setB(&t.ServerTokensOff, body.ServerTokensOff)
		set(&t.LimitConnPerIP, body.LimitConnPerIP)
		set(&t.OpenFileCacheMax, body.OpenFileCacheMax)
		setB(&t.AccessLogOff, body.AccessLogOff)

		// An explicit edit means the values are no longer exactly a
		// preset, so the label stops claiming they are.
		if body.Profile == nil && !body.AutoFill && anyFieldSet(body) {
			t.Profile = config.ProfileCustom
		}

		if err := config.ValidateTuning(t); err != nil {
			return err
		}
		c.NginxSettings.Tuning = t
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	c, _ := s.cfg.Read()

	// The main-context directives go into nginx.conf itself. This is the
	// dangerous edit, and it is done first and separately: if nginx
	// refuses it, ApplyMainTuning has already rolled the file back, and
	// reporting that here is more useful than a generic reload failure
	// twenty lines later.
	out := map[string]interface{}{"ok": true}
	if _, mErr := nginxpkg.ApplyMainTuning(c); mErr != nil {
		out["main_error"] = mErr.Error()
	}

	res := s.autoApply()
	out["applied"] = res.OK
	if res.Error != "" {
		out["apply_error"] = res.Error
	}
	if res.Restored {
		out["apply_restored"] = true
	}
	out["warnings"] = config.TuningWarnings(c.NginxSettings.Tuning,
		c.NginxSettings.WorkerConnections)
	out["main_installed"] = nginxpkg.MainTuningInstalled()
	writeJSON(w, 200, out)
}

// anyFieldSet reports whether the request touched an individual field, as
// opposed to only asking for a profile or an auto-fill.
func anyFieldSet(b tuningReq) bool {
	return b.WorkerProcesses != nil || b.WorkerRLimitNofile != nil || b.MultiAccept != nil ||
		b.KeepaliveTimeout != nil || b.KeepaliveRequests != nil ||
		b.ClientHeaderTimeout != nil || b.ClientBodyTimeout != nil || b.SendTimeout != nil ||
		b.ProxyConnectTimeout != nil || b.ProxyReadTimeout != nil ||
		b.ProxySendTimeout != nil || b.ProxySocketKeepalive != nil ||
		b.UpstreamKeepalive != nil || b.ProxyBufferingOff != nil ||
		b.ClientMaxBodyMB != nil || b.ClientBodyBufferKB != nil ||
		b.LargeClientHeaderKB != nil || b.GzipEnabled != nil ||
		b.GzipCompLevel != nil || b.GzipMinLength != nil ||
		b.SSLSessionCacheMB != nil || b.SSLSessionTimeoutMin != nil ||
		b.SSLECDHCurve != nil || b.ServerTokensOff != nil ||
		b.LimitConnPerIP != nil || b.OpenFileCacheMax != nil || b.AccessLogOff != nil
}

// measuredLinkMbits caches a measurement taken through the endpoint below.
//
// Package-level rather than on the Server because it describes the machine,
// not the session, and because it must survive the operator navigating away
// and coming back. Lost on restart, which is correct: a link speed measured
// last month on a different host is not information.
var measuredLinkMbits int

func (s *Server) handleMeasureLink(w http.ResponseWriter, r *http.Request) {
	// Measuring bandwidth means SENDING TRAFFIC, so it is never done
	// automatically — only when the operator asks. On a metered VPS an
	// unrequested speed test is a real cost, and on a filtered network an
	// unexplained burst of outbound traffic is exactly the sort of thing
	// that draws attention.
	mbits, source, err := health.MeasureLink(r.Context())
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{
			"ok": false, "error": err.Error(), "mbits": 0, "source": "failed",
		})
		return
	}
	measuredLinkMbits = mbits
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "mbits": mbits, "source": source,
	})
}

var _ = strconv.Itoa
