---
status: accepted
date: 2026-07-24
---
# 0036. Installable module path + Gitea release workflow (cross-platform binaries)

- **Status:** accepted — **implemented** (module = gitea path; `.gitea/workflows/release.yml` builds mac/linux/windows on `v*` tags)
- **Date:** 2026-07-24
- **Deciders:** maintainer
- **Related:** reverses the bare-module decision in ADR-0034; builds on ADR-0031 (binary is the tool of record) and an earlier Go-standardization decision.

## Context

ADR-0034 set a bare Go module path (`meshctl`), accepting that the tool could only be
built from a clone. In practice that means browsing to the repo and building by hand to
get the binary on a new machine — friction the user wants gone. Two wants:

1. `go install …@latest` should work (no manual clone/build).
2. A CI workflow that cross-compiles macOS/Linux/Windows and attaches the binaries to the
   Gitea release as assets, so a machine without Go can just download.

`meshctl` is pure Go — all targets cross-compile with `CGO_ENABLED=0` (verified:
darwin/arm64+amd64, linux/amd64+arm64, windows/amd64).

## Decision

1. **Module path = `<gitea-host>/<owner>/meshctl`** (the repo's home). Enables
   `go install <gitea-host>/<owner>/meshctl/cmd/meshctl@latest` (binary name
   `meshctl`). Imports updated accordingly; the bare-module tradeoff in ADR-0034 is void.
2. **`.gitea/workflows/release.yml`** — on a `v*` tag (or `workflow_dispatch` with a tag),
   a matrix job cross-compiles the five targets (`-trimpath -ldflags "-s -w"`,
   `CGO_ENABLED=0`), emits `meshctl_<os>_<arch>[.exe]` + a `.sha256`, and attaches them to
   the tag's release via the **Gitea API with the built-in `GITHUB_TOKEN`** — no
   third-party action beyond `checkout` + `setup-go`, so it doesn't depend on the runner's
   action-mirror for anything exotic. The release is created if missing (first matrix job
   wins; others 409 → ignored); assets upload by release id resolved from the tag.

## Consequences

- Positive: three install paths (README) — `go install`, download a release binary, or
  build from clone. New machines need neither a clone nor a Go toolchain.
- Positive: pure-Go / CGO-free keeps cross-compilation trivial and the release matrix
  simple; Windows included.
- Trade-off: module path now embeds the host — a future repo move means a module-path
  change + import rewrite (mechanical, one sweep). Acceptable; the gitea home is intended
  to be permanent (ADR-0034).
- Ops notes: the workflow assumes the Gitea Actions runner provides `ubuntu-latest` with
  `actions/checkout` + `actions/setup-go` and sets `GITHUB_TOKEN`/`GITHUB_SERVER_URL`/
  `GITHUB_REPOSITORY`/`GITHUB_REF_NAME` (Gitea Actions compat vars). Cut a release by
  pushing a `v*` tag. Not yet exercised against the live runner — first tag push validates
  it.
- Follow-up: optional `-X main.version` stamping + a `meshctl --version`; a combined
  `checksums.txt`; a `latest` convenience alias.
