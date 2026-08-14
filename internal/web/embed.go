// Package web embeds the static frontend assets and serves them,
// along with the JSON API.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:static
var staticFS embed.FS

//go:embed all:templates
var templateFS embed.FS

// StaticFS returns the embedded static subtree.
func StaticFS() fs.FS {
	sub, _ := fs.Sub(staticFS, "static")
	return sub
}

// TemplateFS returns the embedded templates subtree.
func TemplateFS() fs.FS {
	sub, _ := fs.Sub(templateFS, "templates")
	return sub
}
