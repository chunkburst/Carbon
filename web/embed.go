// Package web embeds the built production UI (web/dist) into the Carbon binary so
// the desktop app and `carbon web` can serve it with no external assets. The UI is
// produced by `pnpm build`; a committed dist/.gitkeep keeps `go build ./...` green
// before the UI is built — the SPA handler tolerates a missing index.html.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded UI rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
