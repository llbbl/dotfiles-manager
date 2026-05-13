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

## Aliases

### `dfm alias add <name> <command>`

Append a shell alias to a tracked rc file. Managed entries are grouped inside shared fenced blocks so a set of related aliases shows up as a single visual unit rather than a wall of repeated comment fences. There's one default block for everything, plus optional named groups via `--group`.

```
# >>> dfm:aliases >>>
alias cr='claude --resume'
alias ll='ls -lah'
# <<< dfm:aliases <<<
# >>> dfm:group terraform >>>
alias tf='terraform'
alias tfa='terraform apply -auto-approve'
alias tfplan='terraform plan'
# <<< dfm:group terraform <<<
```

The wrapper uses `#` comments, which work in both POSIX shells (bash/zsh/sh) and fish.

`add` without `--group` targets the default `dfm:aliases` block; `add --group <name>` targets the matching `dfm:group <name>` block. The first add to a given block creates the block in place; subsequent adds append a body line inside it.

Group names must match `[A-Za-z][A-Za-z0-9_-]*` so the value is safe to interpolate into the fence comment.

#### Quoting the command argument

`<command>` is a single shell argument passed to `dfm`. That means **your shell** parses the quoting before dfm ever sees the string — dfm just receives whatever bytes the shell hands it. Once dfm has the string it always wraps it in single quotes inside the rc file (and escapes embedded single quotes correctly per shell family — POSIX uses `'\''`, fish uses `\'`).

In practice:

```sh
# One word, no metacharacters → quotes optional
dfm alias add ll ls

# Multiple words → quote at the invocation so the shell groups them
dfm alias add cr "claude --resume"
dfm alias add g  'git status'

# Shell metacharacters you do NOT want your shell to expand → single quotes
dfm alias add gco 'git checkout $(git branch | fzf)'
dfm alias add ag  'rg --color=always | less -R'

# Embedded single quotes inside the command → double-quote the outside
dfm alias add greet "echo 'hello there'"

# Embedded double quotes → single-quote the outside
dfm alias add wat  'echo "what?"'
```

Either `"..."` or `'...'` at the invocation works for most cases — single quotes are safer when the command contains `$`, backticks, or other shell metacharacters because they suppress expansion. Either way, the value lands in your rc file as `alias <name>='<command>'`.

Flags:

- `--shell <bash|zsh|fish|profile>` — pick which rc file to target (defaults to `$SHELL`).
- `--file <path>` — explicit rc file (overrides `--shell`).
- `--group <name>` — place the entry inside the named `dfm:group <name>` block instead of the default `dfm:aliases` block. Name must match `[A-Za-z][A-Za-z0-9_-]*`.
- `--replace` — overwrite any existing definition of this alias, anywhere it lives (legacy per-alias block, default block, or any group block). Useful for moving an alias from one block to another.
- `--force` — append even if the alias is already defined (creates a duplicate inside the target block) or if the secrets pre-flight flagged the new content. Mutually exclusive with `--replace`.

Exit 4 if the alias is already defined and neither `--replace` nor `--force` was given. Exit 3 on secrets findings (suppressible with `--force`).

Migration note: an earlier dfm version emitted a per-alias fenced block (`# >>> dfm:alias <name> >>>` … `# <<< dfm:alias <name> <<<`). Those legacy blocks coexist with the shared-block format: `dfm alias remove` and the duplicate-detect check still recognise them. Re-running `dfm alias add <name> ... --replace` lifts a legacy entry into the new shared block.

### `dfm alias remove <name>`

Strip every definition of `<name>` from the tracked rc file in one pass: legacy per-alias blocks first, then body lines inside the default and named-group shared blocks (the block itself is dropped if it ends up empty), then any remaining bare-line entries. Exit 4 if no definition exists.

### `dfm alias list`

Best-effort listing of aliases parsed from the tracked rc file. Reads the bare `alias` line inside each shared block (default and named groups) as well as un-fenced legacy entries.

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
