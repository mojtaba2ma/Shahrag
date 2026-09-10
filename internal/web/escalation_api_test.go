package web

// The escalation ladder through the API, which is the only path the panel
// actually uses. The engine tests prove the ladder works; these prove the
// operator can see it, change it, and be stopped from breaking it.

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shahrag/internal/banner"
	"shahrag/internal/config"
)

func putJSONExpect(t *testing.T, s *Server, cookie, path string, body interface{}, want int) string {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("PUT %s -> %d (want %d): %s", path, w.Code, want, w.Body.String())
	}
	return w.Body.String()
}

// A never-configured install must report the recommended ladder, not an
// empty one. This is precisely the shape of the r49 bug: the panel showed
// a switch as on while the engine read the raw stored false.
func TestFreshInstallReportsTheRecommendedLadder(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	var got struct {
		Escalation config.BanEscalation `json:"escalation"`
	}
	getJSON(t, s, tok, "/api/autoban", &got)

	if !got.Escalation.Enabled {
		t.Fatal("a fresh install reports progressive banning off; the panel would render it unticked while the engine escalates")
	}
	if len(got.Escalation.Steps) == 0 {
		t.Fatal("a fresh install reports an empty ladder, so the form would render no steps at all")
	}
	if got.Escalation.Steps[0] != 30 {
		t.Errorf("the first step is %d minutes, want the recommended 30", got.Escalation.Steps[0])
	}
	if got.Escalation.DecayHours != config.DefaultDecayHours {
		t.Errorf("decay is %dh, want %dh", got.Escalation.DecayHours, config.DefaultDecayHours)
	}
}

// What the operator saves is what the engine reads. Round-tripped through
// the real config file rather than asserted on the response, because the
// response is what lied in r49.
func TestASavedLadderReachesTheStoredConfig(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	putJSON(t, s, tok, "/api/autoban", map[string]interface{}{
		"enabled": true,
		"escalation": map[string]interface{}{
			"enabled": true, "steps": []int{10, 60, 600, -1}, "decay_hours": 24,
		},
	})

	c, _ := mgr.Read()
	esc := c.AutoBan.EffectiveEscalation()
	if len(esc.Steps) != 4 || esc.Steps[0] != 10 || esc.Steps[3] != config.PermanentStep {
		t.Fatalf("stored ladder is %v, want [10 60 600 -1]", esc.Steps)
	}
	if esc.DecayHours != 24 {
		t.Errorf("stored decay is %d, want 24", esc.DecayHours)
	}

	var got struct {
		Escalation config.BanEscalation `json:"escalation"`
	}
	getJSON(t, s, tok, "/api/autoban", &got)
	if len(got.Escalation.Steps) != 4 || got.Escalation.Steps[1] != 60 {
		t.Errorf("the API reads back %v, which disagrees with the stored config", got.Escalation.Steps)
	}
}

// Turning it OFF has to stick. Same reasoning as LogBans: a stored false
// that the API keeps reporting as true is a setting the operator cannot
// actually change.
func TestSwitchingTheLadderOffSticks(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	putJSON(t, s, tok, "/api/autoban", map[string]interface{}{
		"enabled":    true,
		"escalation": map[string]interface{}{"enabled": false},
	})

	c, _ := mgr.Read()
	if c.AutoBan.EffectiveEscalation().Enabled {
		t.Fatal("the stored config still has progressive banning on after the operator turned it off")
	}
	var got struct {
		Escalation config.BanEscalation `json:"escalation"`
	}
	getJSON(t, s, tok, "/api/autoban", &got)
	if got.Escalation.Enabled {
		t.Fatal("the API reports progressive banning on after the operator turned it off")
	}
}

// A ladder that cannot work is refused with a message that names the
// problem. "Invalid" teaches an operator nothing.
func TestABrokenLadderIsRefusedWithAReason(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	cases := []struct {
		name  string
		steps []int
		want  string
	}{
		{"descending", []int{120, 30}, "shorter"},
		{"permanent in the middle", []int{30, -1, 120}, "permanent"},
	}
	for _, c := range cases {
		body := putJSONExpect(t, s, tok, "/api/autoban", map[string]interface{}{
			"enabled":    true,
			"escalation": map[string]interface{}{"enabled": true, "steps": c.steps},
		}, 400)
		if !strings.Contains(strings.ToLower(body), c.want) {
			t.Errorf("%s: the refusal does not explain itself: %s", c.name, body)
		}
	}
}

// A save from a client that does not know about the ladder must not switch
// it off for everybody. Older panel JS cached in a browser is exactly this
// case, and it would silently disable the feature on the next save.
func TestASaveWithoutTheLadderFieldDoesNotDisableIt(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	putJSON(t, s, tok, "/api/autoban", map[string]interface{}{
		"enabled": true, "action": "notfound",
	})
	c, _ := mgr.Read()
	if !c.AutoBan.EffectiveEscalation().Enabled {
		t.Fatal("a save from an older client silently switched progressive banning off")
	}
}

// The ledger endpoint. Without it the ladder is invisible: the operator
// sees two addresses with wildly different ban lengths and no way to find
// out why.
func TestTheOffenderLedgerIsServedAndCanBeForgiven(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	dir := t.TempDir()
	banner.StatePath = filepath.Join(dir, "bans.json")
	banner.BanLogPath = filepath.Join(dir, "bans.log")
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = config.DefaultAutoBan()
		c.AutoBan.Enabled = true
		c.AutoBan.Configured = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e := banner.New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "acc.log"), nil)
	e.SetOnChange(nil)
	s.SetBanEngine(e)
	t.Cleanup(func() { s.SetBanEngine(nil) })

	// A manual ban does not climb the ladder — it is the operator's own
	// decision, not an offence — so drive the ledger the way a real
	// offence does, through the engine.
	if err := e.Ban("203.0.113.90", "manual", time.Hour, false); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Enabled    bool                     `json:"enabled"`
		Steps      []int                    `json:"steps"`
		DecayHours int                      `json:"decay_hours"`
		Offenders  []map[string]interface{} `json:"offenders"`
	}
	getJSON(t, s, tok, "/api/autoban/offenders", &got)
	if !got.Enabled {
		t.Error("the ledger endpoint reports progressive banning off on a default install")
	}
	if len(got.Steps) == 0 {
		t.Error("the ledger endpoint does not report the ladder, so the panel cannot explain a level")
	}
	if got.DecayHours != config.DefaultDecayHours {
		t.Errorf("the ledger reports decay %dh, want %dh", got.DecayHours, config.DefaultDecayHours)
	}

	// Forgiving something with no history is a 404, not a silent success:
	// an operator who mistypes an address must not be told it worked.
	req := httptest.NewRequest("DELETE", "/api/autoban/offenders/198.51.100.200", nil)
	req.Header.Set("Cookie", tok)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("forgiving an unknown address returned %d, want 404", w.Code)
	}
}
