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

## PATH entries

The `dfm path` family manages directories on your `PATH` from inside a tracked rc file. Multiple directories per direction collapse into a single managed block, the block is idempotent on re-source (sourcing the rc file N times leaves each managed dir on `PATH` exactly once), and unmanaged installer blocks (pnpm, nvm, mise, rustup) are left byte-identical so dfm and tool installers don't fight.

The on-disk shape for bash/zsh looks like this:

```
# dfm:path:<id8> >>> direction=prepend dirs=/a:/b:/c
for __dfm_d in /a /b /c; do
  case ":$PATH:" in
    *":$__dfm_d:"*) ;;
    *) PATH="$__dfm_d:$PATH" ;;
  esac
done
unset __dfm_d
export PATH
# dfm:path:<id8> <<<
```

`<id8>` is the first eight hex characters of `sha256("<direction>:<dirs>")` and rotates whenever the dir list changes — the marker is data, not an identifier you depend on. There's at most one managed block per direction per rc file (`prepend` and `append` are independent entries); a second managed block in the same direction is treated as corruption and refused (see "Corruption guard" below).

Fish uses the same idea adapted to fish's `if not contains` form; on a fish rc file the block is emitted with fish syntax automatically.

### `dfm path add <dir>`

Add `<dir>` to the dfm-managed PATH entry. The first call creates the block; subsequent calls splice the new dir into the existing block and rotate the marker id.

```sh
dfm path add ~/.local/bin
dfm path add ~/.cargo/bin
dfm path add /opt/homebrew/sbin --append
```

Flags:

- `--shell <bash|zsh|fish|profile>` — pick which rc file to target (defaults to `$SHELL`).
- `--file <path>` — explicit rc file (overrides `--shell`).
- `--append` — write into the append-direction entry (default: prepend).
- `--force` — bypass the secrets pre-flight scan. Does NOT bypass dedup — adding a dir that's already on the managed entry still exits 4.

Exit 4 if the dir is already on the managed entry in the same direction (cross-spelling equivalence: `~/x` and `$HOME/x` are the same dir). Exit 3 on secrets findings (suppressible with `--force`).

### `dfm path remove <dir>`

Remove `<dir>` from the dfm-managed PATH entry. If the entry has multiple dirs the block shrinks; if `<dir>` was the only dir the entire block is dropped (no empty stub left behind).

Flags:

- `--shell <bash|zsh|fish|profile>` — pick which rc file to target.
- `--file <path>` — explicit rc file (overrides `--shell`).

Exit 4 if `<dir>` is not on any dfm-managed entry in the target file.

### `dfm path list`

List the dirs on each dfm-managed PATH entry in the target rc file. Default output is tab-separated rows of `DIR / DIRECTION / MARKER_ID`; `--json` emits a flat array of `{dir, direction, marker_id}` objects.

`list` only enumerates dfm-managed entries — installer blocks (pnpm, nvm, mise, etc.) are intentionally invisible to this command. Use your shell directly (`echo $PATH | tr ':' '\n'`) to see your full effective `PATH`.

Flags:

- `--shell <bash|zsh|fish|profile>` — pick which rc file to target.
- `--file <path>` — explicit rc file (overrides `--shell`).
- `--json` — JSON output.

### `dfm path import`

Scan a tracked rc file for static `PATH` lines and fold them into one dfm-managed prepend entry.

```sh
dfm path import                    # interactive: print proposal, prompt y/N
dfm path import --dry-run          # print proposal, exit without writing
dfm path import --yes              # apply without prompting (for scripts)
```

The scanner classifies every PATH-touching line into one of five buckets:

- **Importable bare prepend** — `export PATH="X:$PATH"` with a literal dir token.
- **Importable guarded prepend** — pnpm-style `case ":$PATH:" in *":$X:"*) ;; *) export PATH="$X:$PATH" ;; esac`.
- **Dynamic skip** — `eval "$(mise activate zsh)"`, `source ~/.nvm/nvm.sh`, plugin-loader sourcing. These run code at startup; dfm can't safely import them.
- **Already-managed skip** — a dfm-managed block (the entire `>>> … <<<` range counts as one entry; it's not re-imported).
- **Unknown** — anything PATH-shaped the classifier doesn't recognise. Surfaced in the proposal so you can act on it manually; never imported.

Apply takes a pre-edit snapshot first, then splices the bare/guarded importable lines out and inserts a single coalesced managed prepend block in their place. The snapshot is the rollback path — see `dfm restore <snapshot-id>` to undo.

Flags:

- `--shell <bash|zsh|profile>` — pick which rc file to target. Fish is not supported by `import` (fish's PATH conventions are different enough that there's nothing to fold).
- `--file <path>` — explicit rc file (overrides `--shell`).
- `--dry-run` — print the proposal and exit without writing.
- `-y`, `--yes` — apply without prompting. Required when stdin is non-interactive (a piped or redirected shell errors out without `--yes` or `--dry-run`).
- `--json` — emit a JSON report instead of human-readable text. JSON mode keeps stdout clean even when the interactive prompt fires (prompt text goes to stderr).

`--dry-run` wins if both `--dry-run` and `--yes` are set. The command refuses if the target rc file already has a dfm-managed prepend entry — clean up with `dfm path remove` first, or add dirs to the existing entry with `dfm path add`.

### Coexistence with installer blocks

dfm's managed PATH block matches the shape pnpm, nvm, mise, and rustup all use — a substring guard so re-sourcing the rc file doesn't re-prepend. Installer blocks are detected and skipped during `dfm path` operations: they aren't enumerated by `dfm path list`, aren't disturbed by `dfm path add`/`remove`/`import`, and stay byte-identical across dfm mutations. You can let installers continue to manage their own dirs; dfm only owns the dirs you explicitly `path add` or import.

### Re-source idempotency

The managed block is built so sourcing your rc file repeatedly never grows `PATH`. Each managed dir is wrapped in its own `case` guard inside a `for` loop, so a dir that's already on `PATH` short-circuits. You can verify this on your own shell:

```sh
echo $PATH | tr ':' '\n' | sort | uniq -c | sort -rn | head
```

Any line with a count > 1 is a duplicate. After moving dirs into a dfm-managed entry and re-sourcing, the managed dirs each appear exactly once regardless of how many times the rc file has been sourced.

### Zsh `typeset -U path` pairing

On zsh you can additionally add `typeset -U path PATH` near the top of `~/.zshrc` — this tells zsh to dedupe `path` automatically on every assignment. It pairs naturally with `dfm path`: dfm makes the managed block itself idempotent, and `typeset -U` cleans up any non-dfm-managed lines (legacy installer blocks, hand-edited exports) that don't have their own guard. The two mechanisms are independent and stack cleanly.

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
