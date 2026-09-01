package mesh

import "embed"

// templatesFS holds the new-agent bootstrap files + the pool doctrine, embedded
// at build time so a $PATH install carries them with no external directory.
// `all:` includes the dotfiles (.iterm2*) that a bare glob would skip.
//
// Everything here is source, edited in place — including the doctrine under
// templates/doctrine/ (agent-comms.md, distiller.md), which the binary emits into
// each pool. (go:embed cannot reach outside this package, so the doctrine lives here
// rather than at the repo root.)
//
//go:embed all:templates
var templatesFS embed.FS
