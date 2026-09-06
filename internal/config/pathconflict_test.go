package config

// Duplicate locations.
//
// Two services on the same hostname, port and path make nginx reject the
// WHOLE configuration file with:
//
//	nginx: [emerg] duplicate location "/" in .../gateway.conf:48
//
// It is not a warning and it does not degrade gracefully: nginx will not
// start or reload, so the next reboot takes down every site on the server,
// not just the two that clash. The panel used to accept this silently.

import (
	"path/filepath"
	"strings"
	"testing"
)

func newCfg(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "config.json")
	LockPath = filepath.Join(dir, "config.lock")
	m := New()
	if err := m.AddDomain("example.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	return m
}

// The exact reported case: two services, one domain, both on "/".
func TestTwoServicesCannotShareARootPath(t *testing.T) {
	m := newCfg(t)
	if err := m.AddService("first", "www", "example.test", 3000, 8443, "/", true, false); err != nil {
		t.Fatalf("the first service should be accepted: %v", err)
	}
	err := m.AddService("second", "www", "example.test", 3001, 8443, "/", true, false)
	if err == nil {
		t.Fatal("a second service on the same host, port and path was accepted — " +
			"this generates a duplicate location and nginx refuses to start")
	}
	// The message has to name the service already there; knowing only the
	// one being rejected does not tell you what to change.
	if !strings.Contains(err.Error(), "first") {
		t.Errorf("the error does not name the conflicting service: %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate location") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}

	c, _ := m.Read()
	if _, ok := c.Services["second"]; ok {
		t.Error("the rejected service was written to the config anyway")
	}
	if len(c.Services) != 1 {
		t.Errorf("expected 1 service, got %d", len(c.Services))
	}
}

// The same clash on a non-root path.
func TestTwoServicesCannotShareANamedPath(t *testing.T) {
	m := newCfg(t)
	_ = m.AddService("a", "app", "example.test", 3000, 8443, "api", true, false)
	if err := m.AddService("b", "app", "example.test", 3001, 8443, "api", true, false); err == nil {
		t.Error("two services were allowed to share /api on the same hostname")
	}
	// A leading slash is stripped by NormalizePath, so "/api" and "api" are
	// the same location and must still be caught.
	if err := m.AddService("c", "app", "example.test", 3002, 8443, "/api", true, false); err == nil {
		t.Error("the same path written with a leading slash slipped through")
	}
}

// Everything that legitimately separates two services must keep working.
func TestServicesMayCoexistWhenSomethingDiffers(t *testing.T) {
	m := newCfg(t)
	if err := m.AddDomain("other.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	_ = m.AddService("base", "www", "example.test", 3000, 8443, "/", true, false)

	cases := []struct {
		name                string
		svc, sub, dom, path string
		port                int
	}{
		{"a different path", "p", "www", "example.test", "admin", 8443},
		{"a different subdomain", "s", "api", "example.test", "/", 8443},
		{"a different domain", "d", "www", "other.test", "/", 8443},
		{"a different listen port", "n", "www", "example.test", "/", 9443},
	}
	for _, tc := range cases {
		err := m.AddService(tc.svc, tc.sub, tc.dom, 4000, tc.port, tc.path, true, false)
		if err != nil {
			t.Errorf("%s should be allowed but was refused: %v", tc.name, err)
		}
	}
}

// A trailing slash is a DIFFERENT nginx location and must not be treated as
// a clash — "/app" and "/app/" can legitimately coexist.
func TestTrailingSlashIsADistinctLocation(t *testing.T) {
	m := newCfg(t)
	_ = m.AddService("a", "www", "example.test", 3000, 8443, "app", true, false)
	if err := m.AddService("b", "www", "example.test", 3001, 8443, "app/", true, false); err != nil {
		t.Errorf("/app and /app/ are different locations and should both be allowed: %v", err)
	}
}

// Hostnames are case-insensitive: APP.Example.test and app.example.test are
// one server block, so they do collide.
func TestHostnameComparisonIsCaseInsensitive(t *testing.T) {
	m := newCfg(t)
	if err := m.AddDomain("Example.test", "/c.pem", "/c.key"); err != nil {
		// Case-variant domains may be collapsed by the manager; either way
		// the service check below is what matters.
		t.Logf("adding a case-variant domain: %v", err)
	}
	_ = m.AddService("a", "www", "example.test", 3000, 8443, "/", true, false)

	c, _ := m.Read()
	probe := Service{
		LocalPort: 3001, ListenPort: 8443, Path: "/",
		Bindings: []Binding{{Domain: "example.test", Subdomain: "WWW"}},
	}
	if err := CheckPathConflict(c, "b", probe); err == nil {
		t.Error("a subdomain differing only in case was not recognised as the same host")
	}
}

// Editing a service must not report it as conflicting with itself.
func TestEditingAServiceDoesNotConflictWithItself(t *testing.T) {
	m := newCfg(t)
	_ = m.AddService("only", "www", "example.test", 3000, 8443, "/", true, false)
	c, _ := m.Read()
	svc := c.Services["only"]
	svc.LocalPort = 9999 // an unrelated edit
	if err := CheckPathConflict(c, "only", svc); err != nil {
		t.Errorf("editing a service reported a conflict with itself: %v", err)
	}
}

// A disabled service occupies nothing, so it cannot block anything — and
// re-enabling it must be refused if the path was taken meanwhile.
func TestDisabledServicesDoNotReserveTheirPath(t *testing.T) {
	m := newCfg(t)
	_ = m.AddService("off", "www", "example.test", 3000, 8443, "/", true, false)
	if _, err := m.Mutate(func(c *Config) error {
		svc := c.Services["off"]
		svc.Disabled = true
		c.Services["off"] = svc
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.AddService("on", "www", "example.test", 3001, 8443, "/", true, false); err != nil {
		t.Fatalf("a disabled service blocked a path it does not generate: %v", err)
	}

	// Now switching the first one back on WOULD clash.
	c, _ := m.Read()
	svc := c.Services["off"]
	svc.Disabled = false
	if err := CheckPathConflict(c, "off", svc); err == nil {
		t.Error("re-enabling a service onto a taken path was allowed")
	}
}

// Adding a binding moves a path onto another hostname, where it may already
// be taken.
func TestAddingABindingIsChecked(t *testing.T) {
	m := newCfg(t)
	if err := m.AddDomain("other.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	_ = m.AddService("a", "www", "example.test", 3000, 8443, "/", true, false)
	_ = m.AddService("b", "www", "other.test", 3001, 8443, "/", true, false)

	// Binding b to example.test puts a second "/" on www.example.test.
	if err := m.AddBinding("b", "www", "example.test"); err == nil {
		t.Error("a binding that creates a duplicate location was accepted")
	}
}

// A service can collide with ITSELF if two bindings resolve to the same
// hostname. That emits the location twice in one server block.
func TestAServiceCannotCollideWithItself(t *testing.T) {
	svc := Service{
		LocalPort: 3000, ListenPort: 443, Path: "/",
		Bindings: []Binding{
			{Domain: "example.test", Subdomain: "www"},
			{Domain: "EXAMPLE.test", Subdomain: "WWW"},
		},
	}
	if err := SelfPathConflict(nil, "dup", svc); err == nil {
		t.Error("two bindings resolving to the same hostname were accepted")
	}
}

// Reality remaps several public ports onto one HTTP port, so two services
// on "different" ports can still land in the same server block.
func TestRealityRemappedPortsAreComparedByEffectivePort(t *testing.T) {
	c := Default()
	c.Domains = map[string]Domain{"example.test": {Cert: "/c", Key: "/k"}}
	c.Reality.Enabled = true
	c.Reality.HTTPPort = 6038
	c.Reality.Services = map[string]RealityService{
		"r": {SNI: "x.test", LocalPort: 5000, Ports: []int{443, 2053}},
	}
	c.Services = map[string]Service{
		"a": {LocalPort: 3000, ListenPort: 443, Path: "/",
			Bindings: []Binding{{Domain: "example.test", Subdomain: "www"}}},
	}
	probe := Service{
		LocalPort: 3001, ListenPort: 2053, Path: "/",
		Bindings: []Binding{{Domain: "example.test", Subdomain: "www"}},
	}
	if err := CheckPathConflict(c, "b", probe); err == nil {
		t.Error("443 and 2053 both remap to the Reality HTTP port, so these " +
			"two services share one server block — the clash was missed")
	}
}

// ValidateServicePaths is what doctor uses on a config that already
// contains a clash.
func TestValidateFindsExistingConflicts(t *testing.T) {
	c := Default()
	c.Domains = map[string]Domain{"example.test": {Cert: "/c", Key: "/k"}}
	c.Services = map[string]Service{
		"a": {LocalPort: 1, ListenPort: 443, Path: "/",
			Bindings: []Binding{{Domain: "example.test", Subdomain: "www"}}},
		"b": {LocalPort: 2, ListenPort: 443, Path: "/",
			Bindings: []Binding{{Domain: "example.test", Subdomain: "www"}}},
		"c": {LocalPort: 3, ListenPort: 443, Path: "ok",
			Bindings: []Binding{{Domain: "example.test", Subdomain: "www"}}},
	}
	got := ValidateServicePaths(c)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %d: %+v", len(got), got)
	}
	if got[0].Existing != "a" || got[0].Incoming != "b" {
		t.Errorf("wrong pair reported: %+v", got[0])
	}
}

// The reported pair must be stable. Ranging over a map without sorting
// would name a different service on each run, which makes the message
// impossible to act on.
func TestConflictReportingIsDeterministic(t *testing.T) {
	c := Default()
	c.Domains = map[string]Domain{"example.test": {Cert: "/c", Key: "/k"}}
	c.Services = map[string]Service{}
	for _, n := range []string{"zebra", "alpha", "mango", "delta"} {
		c.Services[n] = Service{
			LocalPort: 1, ListenPort: 443, Path: "/",
			Bindings: []Binding{{Domain: "example.test", Subdomain: "www"}},
		}
	}
	probe := Service{
		LocalPort: 9, ListenPort: 443, Path: "/",
		Bindings: []Binding{{Domain: "example.test", Subdomain: "www"}},
	}
	first := ""
	for i := 0; i < 40; i++ {
		err := CheckPathConflict(c, "new", probe)
		if err == nil {
			t.Fatal("no conflict reported")
		}
		pc, ok := err.(PathConflict)
		if !ok {
			t.Fatalf("unexpected error type %T", err)
		}
		if first == "" {
			first = pc.Existing
		} else if pc.Existing != first {
			t.Fatalf("the reported service changes between runs: %q then %q",
				first, pc.Existing)
		}
	}
	if first != "alpha" {
		t.Errorf("expected the alphabetically first service, got %q", first)
	}
}

// A service with no bindings generates nothing, so it cannot clash.
func TestServiceWithoutBindingsNeverConflicts(t *testing.T) {
	c := Default()
	c.Services = map[string]Service{
		"a": {LocalPort: 1, ListenPort: 443, Path: "/",
			Bindings: []Binding{{Domain: "example.test", Subdomain: "www"}}},
	}
	probe := Service{LocalPort: 2, ListenPort: 443, Path: "/"}
	if err := CheckPathConflict(c, "b", probe); err != nil {
		t.Errorf("a service with no bindings was reported as conflicting: %v", err)
	}
}
