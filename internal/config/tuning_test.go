package config

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func smallBox() SysInfo {
	// The user's actual server: 957 MB usable, 2 cores, 383 MB swap.
	return SysInfo{
		Cores: 2, RAMBytes: 1003 << 20, AvailableBytes: 420 << 20,
		SwapBytes: 383 << 20, FileMax: 9223372036854775807, NofileHard: 524288,
	}
}

func bigBox() SysInfo {
	return SysInfo{
		Cores: 8, RAMBytes: 16 << 30, AvailableBytes: 12 << 30,
		SwapBytes: 4 << 30, FileMax: 2097152, NofileHard: 1048576,
	}
}

// Every field of Tuning must have a recommendation.
//
// Reflection rather than a hand-written list on purpose: a hand-written list
// is exactly the thing that silently goes stale when somebody adds a field,
// and a field with no recommendation renders an auto-fill button that does
// nothing.
func TestEveryTunableHasARecommendation(t *testing.T) {
	recs := Recommend(smallBox(), ProfileSmall)

	// Fields that deliberately have no recommendation, with the reason.
	skip := map[string]string{
		"Enabled": "the master switch is the operator's decision, not a computed value",
		"Profile": "a label, not a tunable",
	}

	rt := reflect.TypeOf(Tuning{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if _, ok := skip[f.Name]; ok {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if _, ok := recs[tag]; !ok {
			t.Errorf("field %s (json %q) has no recommendation — its auto-fill "+
				"button would do nothing", f.Name, tag)
		}
	}
}

// The recommendations must actually differ between a small box and a large
// one, or the whole exercise is decoration.
func TestRecommendationsAdaptToTheMachine(t *testing.T) {
	small := Recommend(smallBox(), ProfileSmall)
	big := Recommend(bigBox(), ProfileBusy)

	num := func(m map[string]Recommendation, k string) int {
		n, _ := strconv.Atoi(m[k].Value)
		return n
	}

	for _, k := range []string{"worker_connections", "worker_rlimit_nofile",
		"ssl_session_cache_mb", "upstream_keepalive", "limit_conn_per_ip"} {
		s, b := num(small, k), num(big, k)
		if s >= b {
			t.Errorf("%s: small box suggests %d, big box %d — should grow with the machine",
				k, s, b)
		}
	}
	// A small box should hold idle connections for LESS time, not more.
	if num(small, "keepalive_timeout") >= num(big, "keepalive_timeout") {
		t.Errorf("keepalive: small %d, big %d — a small box should let go sooner",
			num(small, "keepalive_timeout"), num(big, "keepalive_timeout"))
	}
}

// worker_rlimit_nofile must always leave room for two descriptors per
// connection. Getting this wrong is the failure where nginx logs "too many
// open files" while its own connection counter says it has capacity.
func TestNofileAlwaysCoversTwoPerConnection(t *testing.T) {
	for _, si := range []SysInfo{smallBox(), bigBox(),
		{Cores: 1, RAMBytes: 512 << 20, AvailableBytes: 200 << 20, NofileHard: 4096},
		{}, // completely unknown machine
	} {
		for _, p := range []string{ProfileSmall, ProfileBalanced, ProfileBusy} {
			r := Recommend(si, p)
			conns, _ := strconv.Atoi(r["worker_connections"].Value)
			nofile, _ := strconv.Atoi(r["worker_rlimit_nofile"].Value)
			if si.NofileHard > 0 && nofile > si.NofileHard {
				t.Errorf("nofile %d exceeds the hard limit %d — nginx would fail to START",
					nofile, si.NofileHard)
			}
			// When the hard limit allows it, the recommendation must
			// cover two per connection.
			if si.NofileHard == 0 || si.NofileHard >= conns*2+1024 {
				if nofile < conns*2 {
					t.Errorf("%v/%s: nofile %d < 2x connections %d",
						si, p, nofile, conns)
				}
			}
		}
	}
}

// A machine we know nothing about must produce conservative numbers, not
// large ones. Guessing high on an unknown box is how a panel makes nginx
// fail to start.
func TestAnUnknownMachineGetsConservativeNumbers(t *testing.T) {
	r := Recommend(SysInfo{}, "")
	conns, _ := strconv.Atoi(r["worker_connections"].Value)
	if conns > 2048 {
		t.Errorf("an unknown machine was recommended %d connections", conns)
	}
	if conns < 512 {
		t.Errorf("an unknown machine was recommended only %d connections", conns)
	}
}

// Every recommendation must carry a reason. A number with no explanation
// cannot be judged or argued with.
func TestEveryRecommendationExplainsItself(t *testing.T) {
	for k, r := range Recommend(smallBox(), ProfileSmall) {
		if r.Why == "" {
			t.Errorf("%s has no reason code", k)
		}
		if strings.Contains(r.Why, " ") {
			t.Errorf("%s: Why should be a translatable CODE, not prose: %q", k, r.Why)
		}
		if r.Value == "" {
			t.Errorf("%s has no value", k)
		}
	}
}

// The access log must never be recommended off: the statistics page and the
// 404-flood ban rule both read it.
func TestTheAccessLogIsNeverRecommendedOff(t *testing.T) {
	for _, p := range []string{ProfileSmall, ProfileBalanced, ProfileBusy} {
		if got := Recommend(smallBox(), p)["access_log_off"].Value; got != "false" {
			t.Errorf("%s recommends access_log_off=%s, which would blind the "+
				"statistics page and the 404 ban rule", p, got)
		}
	}
}

// Proxy read timeout must be long enough for a tunnel. This is the specific
// value that fixes the user's real "recv() failed (104) while proxying
// upgraded connection" error.
func TestProxyReadTimeoutIsLongEnoughForTunnels(t *testing.T) {
	for _, p := range []string{ProfileSmall, ProfileBalanced, ProfileBusy} {
		n, _ := strconv.Atoi(Recommend(smallBox(), p)["proxy_read_timeout"].Value)
		if n < 600 {
			t.Errorf("%s recommends proxy_read_timeout %ds — an idle WebSocket "+
				"would be killed", p, n)
		}
	}
}

func TestApplyRecommendationsFillsEverything(t *testing.T) {
	got := ApplyRecommendations(Tuning{}, Recommend(smallBox(), ProfileSmall))

	if got.WorkerProcesses != "auto" {
		t.Errorf("worker_processes = %q", got.WorkerProcesses)
	}
	if got.WorkerRLimitNofile == 0 {
		t.Error("worker_rlimit_nofile not filled")
	}
	if got.ProxyReadTimeout < 600 {
		t.Errorf("proxy_read_timeout = %d", got.ProxyReadTimeout)
	}
	if !got.GzipEnabled {
		t.Error("gzip not enabled")
	}
	if got.SSLECDHCurve == "" {
		t.Error("ssl_ecdh_curve not filled")
	}
	// The dangerous one must NOT be turned on by an auto-fill.
	if got.AccessLogOff {
		t.Error("auto-fill switched the access log off")
	}
	// And Enabled is the operator's decision, never auto-filled.
	if got.Enabled {
		t.Error("auto-fill switched the whole feature on by itself")
	}
}

func TestValidationRejectsImpossibleValues(t *testing.T) {
	base := ApplyRecommendations(Tuning{Enabled: true}, Recommend(smallBox(), ProfileSmall))
	base.Enabled = true

	if err := ValidateTuning(base); err != nil {
		t.Fatalf("a fully auto-filled config was rejected: %v", err)
	}

	for name, mutate := range map[string]func(*Tuning){
		"gzip level 12":      func(t *Tuning) { t.GzipCompLevel = 12 },
		"negative keepalive": func(t *Tuning) { t.KeepaliveTimeout = -5 },
		"absurd body size":   func(t *Tuning) { t.ClientMaxBodyMB = 99999 },
		"tiny nofile":        func(t *Tuning) { t.WorkerRLimitNofile = 10 },
		"bad worker count":   func(t *Tuning) { t.WorkerProcesses = "many" },
		"injected curve":     func(t *Tuning) { t.SSLECDHCurve = "X25519; root all" },
		"curve with dollar":  func(t *Tuning) { t.SSLECDHCurve = "$host" },
	} {
		v := base
		mutate(&v)
		if err := ValidateTuning(v); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A disabled Tuning is always valid, whatever is in it — otherwise an
// operator could not switch the feature off to escape a bad value.
func TestADisabledTuningIsAlwaysValid(t *testing.T) {
	bad := Tuning{Enabled: false, GzipCompLevel: 99, SSLECDHCurve: "$evil;"}
	if err := ValidateTuning(bad); err != nil {
		t.Fatalf("a disabled tuning was rejected: %v", err)
	}
}

// Zero must always mean "leave nginx's default alone" and never be treated
// as an out-of-range value.
func TestZeroMeansUnsetNotInvalid(t *testing.T) {
	if err := ValidateTuning(Tuning{Enabled: true}); err != nil {
		t.Fatalf("an all-zero enabled tuning was rejected: %v", err)
	}
}

func TestWarningsCatchTheDangerousCombinations(t *testing.T) {
	has := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}

	w := TuningWarnings(Tuning{Enabled: true, AccessLogOff: true}, 1024)
	if !has(w, "access_log_off_breaks_stats") {
		t.Error("switching the access log off produced no warning")
	}

	w = TuningWarnings(Tuning{Enabled: true, WorkerRLimitNofile: 1024}, 4096)
	if !has(w, "nofile_below_connections") {
		t.Error("a descriptor limit below 2x connections produced no warning")
	}

	w = TuningWarnings(Tuning{Enabled: true, ProxyReadTimeout: 60}, 1024)
	if !has(w, "proxy_read_too_short_for_tunnels") {
		t.Error("a 60s proxy read timeout produced no warning")
	}

	// A sensible config must produce NO warnings, or they become noise
	// that nobody reads.
	good := ApplyRecommendations(Tuning{Enabled: true}, Recommend(smallBox(), ProfileSmall))
	good.Enabled = true
	if w := TuningWarnings(good, 1024); len(w) != 0 {
		t.Errorf("the recommended configuration warns about itself: %v", w)
	}
}

// The recommendations must be deterministic: the same machine and profile
// must always produce the same numbers, or the UI would show a different
// suggestion on every refresh.
func TestRecommendationsAreDeterministic(t *testing.T) {
	a := Recommend(smallBox(), ProfileSmall)
	b := Recommend(smallBox(), ProfileSmall)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two identical calls produced different recommendations")
	}
}

// Computing a recommendation must be free: the UI asks for it on every page
// load, and the installer asks during setup.
func TestRecommendIsCheap(t *testing.T) {
	si := smallBox()
	allocs := testing.AllocsPerRun(100, func() { _ = Recommend(si, ProfileSmall) })
	t.Logf("Recommend allocates %.0f times", allocs)
	if allocs > 400 {
		t.Fatalf("Recommend allocates %.0f times, which is more than a "+
			"page-load-frequency call should", allocs)
	}
}
