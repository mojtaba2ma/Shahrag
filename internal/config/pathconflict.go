package config

// Path conflicts.
//
// Two services bound to the same hostname on the same port, both claiming
// the same path, produce two `location <path> { ... }` blocks inside one
// server block. nginx refuses the whole file:
//
//	nginx: [emerg] duplicate location "/" in .../gateway.conf:48
//
// That is not a warning. nginx will not start or reload with such a file,
// so on a server where nginx is restarted for any reason — a reboot, a
// package upgrade — EVERY site goes down, not just the two that clash.
//
// The panel used to accept this silently and only fail later, at generate
// time, with a message naming a line number in a generated file rather than
// the two services responsible. It is refused at save time now, before it
// can reach disk, and the error names both services and the hostname.
//
// Why the check lives here rather than in the web handlers: the CLI menu
// writes through the same Manager. A rule enforced in one caller is a rule
// that the other caller can still break.

import (
	"fmt"
	"sort"
	"strings"
)

// locationKey identifies one nginx location: a hostname, the port its
// server block listens on, and the path.
//
// The port is the EFFECTIVE port. When Reality owns 443 and remaps it to
// 6038, two services on "different" ports 443 and 2053 end up in the same
// server block and do collide — comparing the ports as configured would
// miss it.
type locationKey struct {
	host string // fully-qualified, lowercase
	port int    // effective listen port
	path string // normalised, no leading slash
}

func (k locationKey) String() string {
	p := k.path
	if p == "" || p == "/" {
		p = "/"
	} else {
		p = "/" + p
	}
	return fmt.Sprintf("%s:%d%s", k.host, k.port, p)
}

// fqdn joins a binding's subdomain and domain, lowercased. Hostnames are
// case-insensitive, so "APP.Example.com" and "app.example.com" are the same
// server block and must compare equal.
func fqdn(b Binding) string {
	d := strings.ToLower(strings.TrimSpace(b.Domain))
	s := strings.ToLower(strings.TrimSpace(b.Subdomain))
	if s == "" {
		return d
	}
	return s + "." + d
}

// canonicalPath normalises a path for comparison.
//
// "/" and "" are the same location to nginx. A trailing slash, however, is
// NOT: "/app" and "/app/" are two different locations and may legitimately
// coexist, so it is preserved.
func canonicalPath(p string) string {
	p = NormalizePath(strings.TrimSpace(p))
	if p == "/" {
		return ""
	}
	return p
}

// locationsFor returns every location a service occupies. A service bound
// to three hostnames occupies three.
func locationsFor(c *Config, svc Service) []locationKey {
	if !svc.IsEnabled() {
		// A disabled service generates nothing, so it cannot collide with
		// anything. Refusing a save because of an invisible conflict would
		// be worse than useless: there would be no way to see the cause.
		return nil
	}
	port := svc.ListenPort
	if c != nil {
		port = c.EffectivePort(port)
	}
	path := canonicalPath(svc.Path)
	out := make([]locationKey, 0, len(svc.Bindings))
	for _, b := range svc.Bindings {
		if strings.TrimSpace(b.Domain) == "" {
			continue
		}
		out = append(out, locationKey{host: fqdn(b), port: port, path: path})
	}
	return out
}

// PathConflict describes two services that would produce the same nginx
// location.
type PathConflict struct {
	Existing string
	Incoming string
	Host     string
	Port     int
	Path     string
}

// Error renders the conflict as the message an operator sees. It names both
// services, because knowing only one of them does not tell you what to
// change.
func (p PathConflict) Error() string {
	path := p.Path
	if path == "" {
		path = "/"
	} else {
		path = "/" + path
	}
	return fmt.Sprintf(
		"service %q already serves %s on port %d — two services cannot share "+
			"one path on the same hostname, because nginx refuses the whole "+
			"configuration with \"duplicate location\". Give %q a different path, "+
			"a different subdomain, or a different listen port.",
		p.Existing, path, p.Port, p.Incoming)
}

// CheckPathConflict reports whether `svc`, saved under the name `name`,
// would collide with any other service already in the config.
//
// The service's own current entry is skipped, so editing a service without
// changing its path is not reported as a conflict with itself.
func CheckPathConflict(c *Config, name string, svc Service) error {
	if c == nil {
		return nil
	}
	incoming := locationsFor(c, svc)
	if len(incoming) == 0 {
		return nil
	}
	want := make(map[locationKey]bool, len(incoming))
	for _, k := range incoming {
		want[k] = true
	}

	// Sorted iteration: a map's order is random, so without this the panel
	// would name a different service each time two of them clash.
	others := make([]string, 0, len(c.Services))
	for n := range c.Services {
		if n == name {
			continue
		}
		others = append(others, n)
	}
	sort.Strings(others)

	for _, other := range others {
		for _, k := range locationsFor(c, c.Services[other]) {
			if want[k] {
				return PathConflict{
					Existing: other, Incoming: name,
					Host: k.host, Port: k.port, Path: k.path,
				}
			}
		}
	}
	return nil
}

// SelfPathConflict reports a service that collides with ITSELF: two
// bindings resolving to the same hostname, which would emit the same
// location twice inside one server block just as surely as two services
// would.
func SelfPathConflict(c *Config, name string, svc Service) error {
	seen := map[locationKey]bool{}
	for _, k := range locationsFor(c, svc) {
		if seen[k] {
			return PathConflict{
				Existing: name, Incoming: name,
				Host: k.host, Port: k.port, Path: k.path,
			}
		}
		seen[k] = true
	}
	return nil
}

// ValidateServicePaths checks a whole configuration and returns every
// conflict it finds.
//
// Used by `shahrag doctor` and by the repair path: a config that predates
// this check, or one restored from a backup, can already contain a clash,
// and the operator needs to be told which services to fix rather than being
// left with an nginx that will not start.
func ValidateServicePaths(c *Config) []PathConflict {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.Services))
	for n := range c.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	owner := map[locationKey]string{}
	var out []PathConflict
	for _, n := range names {
		for _, k := range locationsFor(c, c.Services[n]) {
			if prev, ok := owner[k]; ok {
				out = append(out, PathConflict{
					Existing: prev, Incoming: n,
					Host: k.host, Port: k.port, Path: k.path,
				})
				continue
			}
			owner[k] = n
		}
	}
	return out
}
