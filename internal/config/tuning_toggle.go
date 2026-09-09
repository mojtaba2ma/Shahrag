package config

// Per-setting on/off switches.
//
// r47 shipped the advanced block with ONE master switch, and that was a
// design mistake with a real consequence: enabling it applied thirty
// settings at once, and one of them — the per-address connection limit —
// took a live server off the air within minutes. The operator had no way to
// say "I want the long proxy timeouts but not the connection cap", which is
// exactly what they needed.
//
// So every setting now has its own switch. The rules:
//
//   - A setting is applied only when the master switch is on AND that
//     setting's own switch is on.
//   - An ABSENT switch means "use the default for this setting", and the
//     defaults are deliberately not uniform: the safe, boring settings
//     default to on, and anything that can refuse a request defaults to
//     OFF. A tuning panel must not be able to deny service by accident.
//   - Switching a setting off removes it from the generated config
//     entirely, so nginx falls back to its own default rather than to
//     whatever the panel last wrote.
//
// The switch is stored separately from the value so that turning something
// off and on again does not lose the number the operator had typed.

// Setting keys. These match the JSON field names in Tuning, which is what
// lets the UI, the recommendation map and these switches all be keyed the
// same way.
const (
	SetWorkerProcesses      = "worker_processes"
	SetWorkerRLimitNofile   = "worker_rlimit_nofile"
	SetMultiAccept          = "multi_accept"
	SetKeepaliveTimeout     = "keepalive_timeout"
	SetKeepaliveRequests    = "keepalive_requests"
	SetClientHeaderTimeout  = "client_header_timeout"
	SetClientBodyTimeout    = "client_body_timeout"
	SetSendTimeout          = "send_timeout"
	SetProxyConnectTimeout  = "proxy_connect_timeout"
	SetProxyReadTimeout     = "proxy_read_timeout"
	SetProxySendTimeout     = "proxy_send_timeout"
	SetProxySocketKeepalive = "proxy_socket_keepalive"
	SetUpstreamKeepalive    = "upstream_keepalive"
	SetProxyBufferingOff    = "proxy_buffering_off"
	SetClientMaxBodyMB      = "client_max_body_mb"
	SetClientBodyBufferKB   = "client_body_buffer_kb"
	SetLargeClientHeaderKB  = "large_client_header_kb"
	SetGzipEnabled          = "gzip_enabled"
	SetGzipCompLevel        = "gzip_comp_level"
	SetGzipMinLength        = "gzip_min_length"
	SetSSLSessionCacheMB    = "ssl_session_cache_mb"
	SetSSLSessionTimeoutMin = "ssl_session_timeout_min"
	SetSSLECDHCurve         = "ssl_ecdh_curve"
	SetServerTokensOff      = "server_tokens_off"
	SetLimitConnPerIP       = "limit_conn_per_ip"
	SetOpenFileCacheMax     = "open_file_cache_max"
	SetAccessLogOff         = "access_log_off"
)

// AllSettings is every switchable key, in the order the form shows them.
// Used by the UI, by the completeness test, and by DefaultEnabled.
var AllSettings = []string{
	SetWorkerProcesses, SetWorkerRLimitNofile, SetMultiAccept,
	SetProxyReadTimeout, SetProxySendTimeout, SetProxyConnectTimeout,
	SetProxySocketKeepalive, SetUpstreamKeepalive, SetProxyBufferingOff,
	SetKeepaliveTimeout, SetKeepaliveRequests, SetClientHeaderTimeout,
	SetClientBodyTimeout, SetSendTimeout,
	SetClientMaxBodyMB, SetClientBodyBufferKB, SetLargeClientHeaderKB,
	SetGzipEnabled, SetGzipCompLevel, SetGzipMinLength,
	SetSSLSessionCacheMB, SetSSLSessionTimeoutMin, SetSSLECDHCurve,
	SetServerTokensOff, SetLimitConnPerIP, SetOpenFileCacheMax,
	SetAccessLogOff,
}

// dangerousSettings can REFUSE a request or blind the panel's own
// diagnostics. They default to OFF and the UI marks them.
//
// This list is the direct lesson of the r47 incident. limit_conn_per_ip
// looked like an ordinary hardening knob; applied automatically to a server
// whose users all appear to come from one address, it denied service to
// everyone inside two minutes.
var dangerousSettings = map[string]bool{
	SetLimitConnPerIP: true,
	SetAccessLogOff:   true,
	// A short header/body timeout is a slowloris defence, but it also cuts
	// off a genuinely slow mobile client. Safe enough to recommend, not
	// safe enough to switch on without being asked.
	SetClientHeaderTimeout: true,
	SetClientBodyTimeout:   true,
	SetSendTimeout:         true,
}

// IsDangerous reports whether a setting can refuse traffic or hide
// information the panel needs.
func IsDangerous(key string) bool { return dangerousSettings[key] }

// DefaultEnabled returns the shipped switch state: everything on except the
// settings that can deny service.
func DefaultEnabled() map[string]bool {
	out := make(map[string]bool, len(AllSettings))
	for _, k := range AllSettings {
		out[k] = !dangerousSettings[k]
	}
	return out
}

// SettingOn reports whether one setting should be emitted.
//
// Nil or missing entries fall back to the shipped default rather than to
// false, so a config written before per-setting switches existed keeps
// behaving as it did — except for the dangerous ones, which are switched
// off on the way through. That asymmetry is deliberate: an upgrade must
// never silently keep applying the setting that took the server down.
func (t Tuning) SettingOn(key string) bool {
	if !t.Enabled {
		return false
	}
	if t.SettingsEnabled != nil {
		if v, ok := t.SettingsEnabled[key]; ok {
			return v
		}
	}
	return !dangerousSettings[key]
}

// NormalizeSettings fills in any missing switch so the stored config is
// explicit and the UI never has to guess.
//
// Deliberately NOT built from SettingOn: that answers "will this be
// emitted?", which is false for everything while the master switch is off.
// The form needs the opposite question — "what would be applied if it were
// on?" — or every switch renders unticked before the feature is enabled,
// and the operator sees thirty settings that all appear disabled.
//
// Found by the browser test, which read the API and got false for every
// setting on a fresh install.
func (t Tuning) NormalizeSettings() map[string]bool {
	out := make(map[string]bool, len(AllSettings))
	for _, k := range AllSettings {
		if t.SettingsEnabled != nil {
			if v, ok := t.SettingsEnabled[k]; ok {
				out[k] = v
				continue
			}
		}
		out[k] = !dangerousSettings[k]
	}
	return out
}
