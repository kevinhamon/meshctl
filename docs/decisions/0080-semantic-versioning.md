---
status: accepted
date: 2026-10-28
---
# 0080. Semantic versioning: what counts as major, minor and patch

- **Status:** accepted
- **Date:** 2026-10-28
- **Deciders:** maintainer (accepted 2026-10-28)
- **Related:** `.github/workflows/release.yml`, `go.mod`, `internal/mesh/settings.go` (`MeshWiringVersion`)

## Context

Releases have not chosen between minor and patch consistently. Several `feat:` releases shipped as patches (v1.1.3, v1.2.1, v1.7.1, v1.7.2, v1.8.2, v1.9.1), and one `fix:` release shipped as a minor (v1.5.0). Nothing defined what a bump means.

The Go module rules looked like the cause, but they constrain only the major version. From v2 on the module path must end in `/v2`, or `go install github.com/kevinhamon/meshctl/cmd/meshctl@latest` stops working. Minor and patch numbers are unconstrained. meshctl also has no library surface: its packages are under `internal/` and cannot be imported. Its contract is what people and agents rely on from the binary.

## Options considered

1. **Leave it to judgment per release.** Rejected: that is what produced the drift.
2. **Bump minor for everything and keep patch for emergencies.** Rejected: a patch then says nothing, and `meshctl update` users lose the signal that a release is fixes only.
3. **Semantic versioning against an explicit contract, checked at release.** Chosen.

## Decision

- **The contract.** It covers CLI commands and flags, `--json` output shapes, on-disk formats (pool layout, `agent.yaml`, knowledge base and ledger files), the wiring version, and console URLs a person may bookmark. The console's internal HTTP API and anything under `internal/` are not part of it.
- **Major** is a change to the contract that `pool upgrade` cannot absorb, such as removing or renaming a command or flag, or changing an on-disk format with no migration. A major release also moves the module path to `/vN` so `go install` keeps working. That cost is a reason to keep breaks rare, not to report new features as patches.
- **Minor** is any new capability a person or agent could notice: a new command, flag, console feature or doctor check. A change to the wiring version is always at least minor, because every agent then needs `pool upgrade`.
- **Patch** fixes behaviour that already exists and adds no new capability. Bug fixes, performance work, docs, CI and wording changes are patches. A patch never changes the wiring version.
- **The release commit agrees with the bump.** The release commit is a squash, and its conventional-commit prefix names the highest kind of change it carries. Any `feat:` makes the release minor. Only `fix:`, `perf:`, `docs:` or `ci:` makes it a patch. This is checked before the tag is cut.
- **Past tags stand.** A tag is never deleted or moved to fix its number, because GitHub turns a re-pushed release into a hidden draft. The rules apply from the release after v1.11.0.

## Consequences

- Positive: a patch from `meshctl update` can be taken without reading the notes; minor releases say there is something new to look at, and wiring bumps are never hidden in a patch.
- Negative: minor numbers climb faster, and the history before v1.11.0 still carries the old drift.
- Follow-ups: none. A tag-time check in CI could compare the prefix with the bump later if the manual check slips.
