package mesh

import "embed"

// templatesFS holds the new-agent bootstrap files + the pool doctrine, embedded
// at build time so a $PATH install carries them with no external directory.
// `all:` includes the dotfiles (.iterm2*) that a bare glob would skip.
//
// The doctrine under templates/doctrine/ is a COPY of the repo-root doctrine/
// (agent-comms.md, distiller.md — the human-authored source of truth). Keep them in
// sync before building/releasing — `go generate ./...` (or the Taskfile `sync-doctrine`
// target) re-copies doctrine/ → templates/doctrine so the binary never ships stale doctrine.
//
//go:generate sh -c "cp ../../doctrine/agent-comms.md ../../doctrine/distiller.md templates/doctrine/"
//go:embed all:templates
var templatesFS embed.FS
