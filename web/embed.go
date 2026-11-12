// Package web holds the built desktop/dashboard UI (web/dist), embedded into
// the Go binaries. Run `bun run build` in web/ before building Go.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var Dist embed.FS

// FS returns the dist directory as the root of the file system.
func FS() fs.FS {
	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether the UI was built (dist/index.html exists).
func Built() bool {
	_, err := fs.Stat(Dist, "dist/index.html")
	return err == nil
}
