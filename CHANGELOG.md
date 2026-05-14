# Changelog

All notable changes to this project will be documented in this file.
## [1.4.0] - 2026-05-14

### Features

- init: Interactive first-run wizard for config + state + repo
## [1.3.0] - 2026-05-14

### Features

- changelog: Generate and maintain CHANGELOG.md (#41)
- state: Orphan prune + state import for cross-DB workflows (#42)

### Refactoring

- apply: Collapse failure-audit branches into helper
- fsx: Unify atomic-write into internal/fsx
## [1.2.1] - 2026-05-14

### Bug Fixes

- migrate: Report initial migration accurately on fresh DB
## [1.2.0] - 2026-05-14

### Features

- runtime: Opt-in .env loading at startup (#37)

### Refactoring

- tracker: Extract RecordHashChange + TakePreEdit helpers
- cmd/dfm: Consolidate test bootstrap into newTestEnv
## [1.1.0] - 2026-05-14

### Documentation

- Add install-from-releases guide (#33)

### Features

- suggest: Require explicit hunk-header ranges in claude prompt (#31)
## [1.0.2] - 2026-05-14

### Bug Fixes

- suggest: Reject malformed diffs before insert (#32)
## [1.0.1] - 2026-05-14

### Bug Fixes

- apply: Tolerate bare @@ hunk headers + JSON encoder DRY (#34)
## [1.0.0] - 2026-05-13

### Features

- log: Silence goose, add DFM_LOG_LEVEL, rename DOTFILES_LOG_* → DFM_LOG_*
## [0.3.0] - 2026-05-13

### Features

- alias: Share managed entries in default + named-group blocks
## [0.2.0] - 2026-05-13

### Features

- alias: Pre-check for duplicates, add --replace flag
- alias: Wrap managed entries in fenced comment blocks

### Miscellaneous

- Add conventional-commits auto-release workflow
- Add Dependabot for gomod and github-actions
- deps: Bump the actions group with 4 updates
## [0.1.0] - 2026-05-12

### Documentation

- Add FSL-1.1-MIT license
- Add .env.example and config.example.toml
- Package and exported-symbol godoc sweep
- Public docs folder and lean README

### Features

- cli: Cobra skeleton and TOML config loader (M1)
- store: LibSQL state store with goose migrations (M2)
- secrets: Heuristic pre-flight scanner for tracked files
- tracker: Track / untrack / list / status commands (M3)
- snapshot: Pre-modification backup/snapshot system (M3.5)
- sync: Private backup repo + audit log (M4)
- ai: Claude Code adapter for ask and suggest
- apply: Apply / reject suggestions and lifecycle history
- log: Structured debug logger via log/slog

### Miscellaneous

- Initial scaffold for dotfiles-manager
- Ignore dolt/beads runtime files
- Stop pinning goose in mise
- Add scripts/backup-dotfiles.sh pre-manager snapshot helper
- Ignore SQLite/libSQL sidecar files

