package console

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui
var uiFS embed.FS

// UI serves the console page (ticket 04) from the embedded ui/ folder.
func UI() http.Handler {
	sub, _ := fs.Sub(uiFS, "ui")
	return http.FileServer(http.FS(sub))
}
