# Architecture

A quick tour of the moving parts. The codebase is small enough that you can read every package in an afternoon; this doc points at the seams.

## At a glance

```
                      ┌─────────────────┐
                      │   cmd/dotfiles  │   cobra commands; thin glue
                      └────────┬────────┘
                               │
        ┌────────────┬─────────┼──────────┬──────────────┐
        ▼            ▼         ▼          ▼              ▼
   internal/    internal/  internal/  internal/      internal/
   tracker      snapshot   apply      ai             vcs
       │            │         │          │              │
       │            │         │          ▼              ▼
       │            │         │   internal/ai/   internal/audit
       │            │         │   claudecode    + internal/dlog
       │            │         │      (shell)
       └──────┬─────┴────┬────┘
              ▼          ▼
       internal/store (libSQL via tursogo + goose migrations)
       internal/config (TOML + env overrides)
```

Every package is intentionally small. The interesting decisions live in the seams between them.

## State store: `internal/store` + `internal/config`

State lives in a libSQL database — either an embedded file at `~/.local/share/dotfiles/state.db` (default) or a remote Turso instance when `TURSO_DATABASE_URL` is set. The driver is [`turso.tech/database/tursogo`](https://docs.turso.tech/sdk/go/quickstart), pure-Go (no cgo), supporting both local file and remote URLs through a single `sql.Open("turso", …)` interface.

Migrations are managed by [`pressly/goose`](https://github.com/pressly/goose) as a library (no goose CLI required at runtime). SQL files live under `internal/store/migrations/` and are embedded into the binary via `go:embed`. `dotfiles migrate {status,up,down,redo}` exposes the same operations to operators.

Tables today:

- `tracked_files` — the canonical set of dotfiles under management (one row per file).
- `suggestions` — AI-generated improvements with `pending | applied | rejected | stale` status.
- `actions` — the audit-log mirror (see below).
- `snapshots` — metadata for the on-disk snapshot store.

`internal/config` loads `~/.config/dotfiles/config.toml`, applies env overrides (notably `TURSO_DATABASE_URL`, `TURSO_AUTH_TOKEN`, `DOTFILES_LOG_*`), and exposes the result via context to every command.

## Tracker: `internal/tracker`

Resolves user-supplied paths to canonical absolute form, rejects directories and system paths (`/etc`, `/usr`, `/var`, `/System`, `/Library`, with temp dirs exempted), refuses symlinks that escape `$HOME` / cwd / tmp roots, and persists `tracked_files` rows. The secrets pre-flight (see below) runs before any row is inserted.

`Track` exposes an `AfterCommit` hook so the CLI layer can trigger an initial snapshot without the tracker package importing `internal/snapshot` — decoupling that keeps both packages small.

## Secrets pre-flight: `internal/secrets`

Heuristic scanner with nine regex rules: AWS keys, GitHub tokens, JWTs, PEM-style private keys, `.env`-shaped assignments, Slack tokens, OpenAI and Anthropic keys (with each other's prefix suppressed to avoid double-flagging). Files larger than 1 MiB are skipped; binary detection on the first 8 KiB short-circuits before any regex runs. Findings include masked excerpts only — the raw matched bytes never appear in stdout, JSON output, or audit records. `dotfiles track` calls into the scanner and refuses to add a flagged file unless `--force` is passed.

## Snapshot system: `internal/snapshot`

Content-addressed blob store under `~/.local/share/dotfiles/backups/`. Files are deduped by SHA-256 — identical content is written once, with each `snapshots` row pointing at the shared blob. Atomic writes: `<dest>.tmp` then rename. Every newly written blob is read back and re-hashed; on mismatch the temp file is removed and an `ErrChecksumMismatch` returned.

Snapshots fire automatically on `track` (initial), on `apply` (pre-apply), and on `sync` (pre-sync). Manual `dotfiles backup` is always available. `dotfiles restore` writes a snapshot's bytes back to disk atomically; the source mode bits are inherited when the file still exists.

`dotfiles prune` evicts by retention window then by size cap, but always preserves the most recent snapshot per file. Blob files are ref-counted in-memory during prune and only removed when no row points at them.

## Apply pipeline: `internal/apply`

In-process unified-diff applier — no shell-out to `patch` or `git apply`. The diff is parsed, validated, and applied to the file's bytes in memory. Hunk offset tolerance is bounded (±3 lines) to prevent silent re-anchoring. Multi-file diffs are rejected. `\ No newline at end of file` is honored. CRLF vs LF line endings are preserved.

The full apply flow is:

1. Load suggestion row and resolve the tracked file.
2. Read current file contents.
3. **Validate the diff** (fails here mean nothing has been touched yet).
4. **Take a pre-apply snapshot** (the safety net).
5. Apply hunks in memory; atomic temp + rename to the canonical path.
6. Update `tracked_files.last_hash` with the new SHA-256.
7. Set `suggestions.status='applied'` + `decided_at=now`.

If any step after the snapshot fails, the source file is left untouched, the suggestion stays `pending`, the error wraps a `PostSnapshotError` carrying the snapshot id, and an `apply_failed` audit record is emitted with a `dotfiles restore <snap-id>` hint.

`dotfiles reject <id>` is the no-op decision counterpart.

## AI provider: `internal/ai` + `internal/ai/claudecode`

`internal/ai` exposes a small `Provider` interface (`Ask`, `Suggest`). `internal/ai/claudecode` is the only concrete implementation today and shells out to the local `claude` CLI:

```
claude -p "<prompt>" --output-format=json [--model <m>] [extra args...]
```

The subprocess environment is scrubbed to a small allowlist (`PATH`, `HOME`, `USER`, `SHELL`, `TMPDIR`, `CLAUDE_*`, `ANTHROPIC_*`). Turso credentials, GH tokens, and unrelated env vars cannot reach the subprocess.

`Suggest` uses a marker-delimited prompt (`---SUMMARY---` / `---DIFF---` / `---END---`) so the response can be split deterministically, and the diff is validated to start with `--- a/` or `diff --git ` before it ever lands in the database. The Provider interface is designed to admit future adapters (Codex, GitHub Copilot CLI, Gemini CLI) without touching callers.

## Backup repo + sync: `internal/vcs`

Thin shell-out wrapper around the system `git` binary. The subprocess environment is scrubbed to a minimal allowlist; commit identity is pinned via `-c user.name`/`-c user.email` so user-global git config doesn't bleed in; force-pushes use `--force-with-lease`, never bare `--force`.

`dotfiles sync` enumerates tracked files, snapshots each one with `ReasonPreSync`, writes mirrored copies into the backup repo at `files/<sanitized-path>`, appends per-file and summary records to the backup repo's own `logs/actions.jsonl`, commits with a structured message, and pushes. Drift between local and remote backup repos is resolved by an explicit strategy (`auto` / `keep-local` / `keep-remote` / `abort`) or by an interactive three-way prompt.

Backup paths sanitize to `files/<rel-to-home>` for `$HOME`-rooted files and `files/_abs/<stripped>` for everything else. Username never appears.

## Audit log: `internal/audit`

The audit log records *user-visible* events — anything a user might want to look back at later. It dual-writes to a JSONL file (durable, append-only) and the libSQL `actions` table (queryable via `json_extract`). The backend is configurable via `[log].backend`:

- `both` (default) — JSONL + libSQL.
- `jsonl` — file only.
- `db` — table only.
- `none` — disabled.

`DOTFILES_LOG_BACKEND` overrides the TOML value. The backup repo's own `logs/actions.jsonl` is written by `dotfiles sync` unconditionally — that file is the committed durable trail and is independent of the local-log backend choice.

Records always carry typed attributes (suggestion id, file id, display path, snapshot id, hashes, durations, exit codes). Diff bodies, prompt bodies, response bodies, and file contents never appear.

## Debug log: `internal/dlog`

Separate from the audit log. The debug log is for developers tracing internal flow when something breaks. It uses `log/slog` and is off by default — shipped binaries are silent unless an operator opts in via `DOTFILES_LOG_LEVEL`. See [Development > Environment variables](./development.md#environment-variables) for the knobs.

## Sortable IDs: `internal/ids`

A 26-character lowercase base32 ID generator (8 ns-timestamp bytes + 8 crypto/rand bytes). Sortable by creation time, no third-party dep. Used by the snapshot and suggestion subsystems.

## Diff rendering: `internal/diffrender`

ANSI-colored unified-diff rendering. One package, two callers (`suggest` and `apply`). The hunk header gets a distinct color from the +/− lines. Plain text when stdout is not a TTY.
