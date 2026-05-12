# Commands

Run `dfm --help` for the live list. Every subcommand accepts `--help` for its own flag set.

Persistent flags:

- `--config <path>` — point at an alternate `config.toml` instead of `$XDG_CONFIG_HOME/dotfiles/config.toml`.
- `-v, --verbose` — verbose output.

## Tracking

### `dfm track <path>`

Begin managing a file. Computes a SHA-256, records it in the `tracked_files` table, takes an initial pre-modification snapshot, and runs a secrets pre-flight that refuses obviously sensitive files unless overridden.

Flags:

- `--force` — track even if the secrets scanner flagged the file or the suffix looks like a binary (`.so`, `.dylib`, `.exe`, etc.).
- `--reset` — re-track an existing file; refresh hash + added_at.
- `--display <path>` — override the human-facing display path (default: `~`-relative when under `$HOME`).

### `dfm untrack <path>`

Stop managing a file. Accepts canonical, `~`-prefixed, or relative path forms.

### `dfm list`

List tracked files. `--json` for machine-readable output. The table shows the display path, hash prefix, and timestamps.

### `dfm status [<path>]`

Compare current file contents to last-known hash. Reports `clean`, `modified`, `missing`, or `new` per file. Exit code 0 when every tracked file is clean, 1 otherwise (`git status --porcelain` convention).

### `dfm scan <path>`

Run the heuristic secrets scanner against a file without tracking it. Matches AWS keys, GitHub tokens, JWT-shaped strings, `.env`-style assignments, and a handful of other patterns. Findings print with the matched secret masked. Exit 3 if any finding, 0 if clean.

## Backup repo + sync

### `dfm init`

First-run setup. Probes `[repo].remote` with `git ls-remote`:

- Remote reachable and non-empty → clone it into `[repo].local`.
- Remote reachable but empty → with `--create-remote` (or interactive confirm), run `gh repo create --private`, push an initial commit.
- Remote unreachable → exit 2 with the underlying git error.

Flags:

- `--remote <url>` — override `[repo].remote`.
- `--create-remote` — accept the gh-create flow non-interactively.
- `--yes` — skip the interactive confirm.

### `dfm sync`

Mirror every tracked file into the backup repo under `files/<sanitized-path>`, take a pre-sync snapshot of each, append per-file and summary records to `logs/actions.jsonl`, commit with a structured message, and push.

Flags:

- `--dry-run` — show the plan without writing or pushing.
- `--message <msg>` — override the commit subject line.
- `--strategy auto|keep-local|keep-remote|abort` — how to resolve drift when the local backup repo diverges from the remote. `auto` fast-forwards if possible; falls back to an interactive three-way prompt if both ahead and behind. Non-TTY + divergence exits 6 unless an explicit strategy is set.
- `--json` — emit a structured summary.

### `dfm log [<file>]`

Show change history. Reads from the libSQL `actions` table primarily; `--with-commits` interleaves matching commits from the backup repo by timestamp.

Flags:

- `--since <date>` — filter to records on/after a date.
- `--limit N` — cap the result count.
- `--suggestion <id>` — show the full lifecycle of a single AI suggestion (suggest → apply or reject), in ascending timestamp order.
- `--with-commits` — interleave git commits from the backup repo.
- `--json` — JSON output.

## AI suggestions

### `dfm ask "<question>"`

Free-form question to the configured AI provider (default: Claude Code). Prints the response; no rows written, no diff produced.

Flags: `--json`.

### `dfm suggest <file>`

Ask the AI to propose improvements to a tracked file. Returns a one-line summary and a unified diff stored in the `suggestions` table with `status='pending'`. The terminal preview is ANSI-colored when stdout is a TTY.

Flags:

- `--goal "<goal>"` — steer the suggestion ("tighten error handling", "modernize comments", etc.). Default is a general "improve readability, correctness, and conventions" goal.
- `--json` — JSON output including the suggestion id, summary, diff, provider, and creation time.

### `dfm suggestions`

List suggestion rows. Default filter is `--status pending`.

Flags:

- `--status pending|applied|rejected|all` — status filter.
- `--file <path>` — only show suggestions for one file.
- `--json` — JSON output.

### `dfm apply <suggestion-id>`

Preview, snapshot, and apply a pending suggestion in-process. The unified diff is parsed and applied directly (no shell-out to `patch`). Before the file is written, the pre-apply snapshot is captured so any mistake is reversible via `dfm restore`.

Flags: `--yes` (skip confirmation), `--json`.

If anything fails after the snapshot is taken, the source file is left untouched, the suggestion stays `pending`, and the error message includes the snapshot id with a `dfm restore` hint.

### `dfm reject <suggestion-id>`

Mark a pending suggestion as rejected without applying it.

## Snapshots

Snapshots are content-addressed blobs under `~/.local/share/dotfiles/backups/`. They land automatically on `track` (initial) and `apply` (pre-apply), and can be taken manually at any time. Restoring is always available.

### `dfm backup <path>`

Take a manual snapshot of a file (does not need to be tracked).

Flags: `--reason manual|pre-apply|pre-sync`, `--json`.

### `dfm backups [<path>]`

List snapshots. With a path argument, restricts to that file.

Flags: `--json`.

### `dfm restore <snapshot-id>`

Restore a snapshot's contents to disk. Atomic temp + rename; refuses to overwrite an existing destination unless told to.

Flags: `--to <path>` (default: snapshot's original path), `--overwrite`, `--json`.

### `dfm prune`

Evict snapshots by retention window + total-size cap. The most recent snapshot per file is always preserved.

Flags: `--dry-run`, `--json`.

## State database

### `dfm migrate <status|up|down|redo>`

Manage goose migrations against the configured state store (`[state].url` or `TURSO_DATABASE_URL`).

## Configuration

### `dfm config show`

Print the effective config as TOML after defaults + env overrides are applied. Useful for sanity-checking which values the binary actually sees.

### `dfm config path`

Print the resolved config path.

## Misc

### `dfm version`

Print the binary's version (build-time ldflags-injected from the current git tag, or `dev`).

### `dfm completion <shell>`

Generate shell completion (cobra default; bash/zsh/fish/powershell supported).
