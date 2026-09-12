---
name: manage
version: 0.1.0
description: Track, back up, and safely modify dotfiles with the dfm CLI — managed rc-file blocks, snapshots, and reviewable AI patches
allowed-tools: Bash, Read, Grep, Glob
---

# /dfm:manage

Drive the `dfm` CLI to manage a user's dotfiles: track files, own `PATH` and alias
entries inside their rc files, take and restore snapshots, mirror everything to a
private backup repo, and review AI-proposed patches.

`dfm --help` and [docs/commands.md](../../docs/commands.md) are the flag reference.
This skill is the judgment that reference does not carry — what will quietly break
if you get it wrong.

## Before anything else

```sh
dfm version          # confirm the binary is on PATH
dfm config path      # which config file is in play
dfm list             # what is already tracked
dfm status           # clean / modified / missing / new per file
```

Do **not** run `dfm config show` to orient yourself. It prints `[state].auth_token`
in cleartext, and anything you print lands in a transcript. `dfm config path` plus
reading the specific key you need is enough.

## The rules that matter

### Managed blocks are generated. Never hand-edit them.

`dfm` owns the text between its markers. Editing inside them by hand is silently
overwritten on the next `dfm` write, and a malformed fence is treated as corruption
and refused.

```
# >>> dfm:aliases >>>            …  # <<< dfm:aliases <<<
# >>> dfm:group <name> >>>       …  # <<< dfm:group <name> <<<
# dfm:path:<id8> >>> …           …  # dfm:path:<id8> <<<
```

Change them with `dfm alias add` / `dfm alias remove` / `dfm path add` /
`dfm path remove`. Everything *outside* the markers is the user's, and `dfm` leaves
it byte-identical — including installer blocks from pnpm, nvm, mise, and rustup,
which are detected and deliberately skipped.

`<id8>` in a path marker is `sha256("<direction>:<dirs>")` truncated to 8 hex chars.
It rotates whenever the dir list changes. It is data, not a handle — never store it
or match on it.

There is at most one managed path block per direction per file. `prepend` and
`append` are independent. A second block in the same direction is corruption and the
command will refuse rather than guess.

### Snapshots are the undo, and they are automatic

Every mutation takes a pre-edit snapshot first. When something goes wrong, reach for
the snapshot instead of reconstructing the file by hand:

```sh
dfm backups ~/.zshrc     # newest first
dfm restore <id>         # any unambiguous id prefix works
```

The ID column in `dfm backups` is truncated for width. Prefixes resolve, so copying
straight out of the table is fine; an ambiguous prefix lists the candidates and exits
7. Use `--json` when you need the full 26-character ID.

### The state DB is local SQLite *or* remote Turso, and switching does not migrate rows

This is the single most common source of confusion with this tool. A user who
switches backends sees an empty snapshot list and concludes their history is gone.
It is not — it is sitting in the other backend. Check which one is active
(`[state].url` in the config file) before diagnosing anything as missing data.

### Destructive commands have a fixed contract

Preview, then default-no confirmation, `--yes` to proceed, `--dry-run` to stop
short. `--dry-run` wins when both are set. Non-interactive stdin without `--yes` or
`--dry-run` is an error, by design — do not work around it by piping `yes`.

Always show the user the `--dry-run` output before running the real thing.

### Prefer `--json`

Nearly every listing command takes `--json`. Parse that, not the human table — the
tables truncate IDs and are formatted for eyes.

### Exit codes carry meaning

Branch on the code, never on message text. `dfm status` follows the
`git status --porcelain` convention: 0 when every tracked file is clean, 1 otherwise.
Command-specific codes are documented per command in `docs/commands.md`.

## Common workflows

### Start managing a file

```sh
dfm track ~/.zshrc
```

Runs a secrets pre-flight and refuses files that look sensitive. If it refuses, read
the finding before reaching for `--force` — a tracked file gets mirrored to the
backup repo, so a false negative there is a secret in git history.

### Move hand-written PATH lines under management

```sh
dfm path import --dry-run     # read the proposal first, always
dfm path import               # interactive confirm
```

The proposal sorts every PATH-touching line into importable, dynamic-skip
(`eval "$(mise activate zsh)"` and friends, which run code at startup and cannot be
folded), already-managed, or unknown. Unknown lines are surfaced and never imported —
walk the user through those by hand.

Import refuses if a managed prepend entry already exists. Add to the existing entry
with `dfm path add` instead.

### Add a directory or alias

```sh
dfm path add ~/.local/bin
dfm path add /opt/homebrew/sbin --append
dfm alias add ll 'ls -lah'
dfm alias add tf 'terraform' --group terraform
```

`--shell <bash|zsh|fish|profile>` or `--file <path>` picks the target rc file;
the default follows `$SHELL`. `~/x` and `$HOME/x` are recognized as the same
directory, so re-adding one spelling of an existing entry exits 4.

Quoting is the usual trap for alias commands: single-quote anything with shell
metacharacters so the user's shell does not expand them at invocation time.

### Edit a tracked file

Use `dfm` rather than writing to the file directly, so the snapshot is taken:

```sh
dfm edit ~/.zshrc            # snapshot, then open in $EDITOR
dfm append ~/.zshrc "..."    # snapshot, then append
```

A direct write with a text editor or a shell redirect skips the snapshot, and
`dfm status` will report the file as `modified` with no rollback point.

To check a file for secrets without tracking it:

```sh
dfm scan ~/.netrc     # exit 3 if anything is found, 0 if clean
```

### Back up

```sh
dfm sync --dry-run
dfm sync
```

Mirrors tracked files into the private backup repo, snapshots each, appends to the
audit log, commits, and pushes. On divergence with the remote, `--strategy` decides;
a non-TTY with divergence and no explicit strategy exits 6.

### Review an AI suggestion

```sh
dfm suggest ~/.zshrc
dfm suggestions
dfm apply <id>      # or: dfm reject <id>
```

Suggestions are diffs, not applied changes. Show the user the diff and let them
decide — do not auto-apply. `apply` snapshots before writing, and on failure leaves
the file untouched and the suggestion `pending`.

## What not to do

- Do not hand-edit inside dfm markers, or reorder a managed block's lines.
- Do not run `dfm config show`, which prints the auth token in cleartext.
- Do not read or list `~/.lsm/` — it holds a private age key.
- Do not print tokens, or a libSQL URL beyond `scheme://host`.
- Do not run `dfm migrate`, `dfm prune`, or `dfm sync` speculatively. They are
  operational actions on real user data, not diagnostics.
- Do not use the user's real dotfiles as a scratch fixture.
