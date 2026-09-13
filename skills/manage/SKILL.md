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
dfm config show      # effective config, credentials redacted
dfm list             # what is already tracked
dfm status           # clean / modified / missing / new per file
```

`dfm config show` is safe: the auth token prints as `<redacted>` and the state URL is
trimmed to `scheme://host`. Never add `--show-secrets` — it prints the real credential,
and anything you print lands in a transcript.

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

Aliases only work at an interactive prompt. Non-interactive bash — `bash script.sh`,
`#!/bin/bash`, `bash -c` — does not expand aliases unless the script sets
`shopt -s expand_aliases`, and even then only for lines parsed after the definition.
zsh does expand them in scripts, so a user who tests there will be surprised by bash.
If the user wants it to work in a script, tell them to use a shell function or a real
executable on `PATH`, not an alias.

### Inspect the generated PATH fragment

```sh
dfm path fragment            # the target's own family
dfm path fragment --shell zsh # POSIX syntax, for sh, bash and zsh
dfm path fragment --fish     # fish syntax
```

Read-only: it re-renders the target rc file's managed entries as a standalone
sourceable file and prints it. `env.sh` covers sh, bash and zsh because fish cannot
source POSIX syntax and one file has to serve the other three; `env.fish` covers fish.
Which syntax you get follows the target, so a bare `dfm path fragment` prints the fish
fragment when `$SHELL` is fish; `--fish` forces it from any target. An rc file with no
managed entries prints just the header.

### Install the hook that sources the fragment

```sh
dfm path hook install --dry-run   # name every file that would change
dfm path hook install             # write the fragment, install the hooks
dfm path hook remove              # strip the hooks again
```

Install writes the fragment to `$XDG_CONFIG_HOME/dotfiles/` and adds a fixed
`# >>> dfm:env >>>` block that sources it. The block resolves the path at shell
startup and guards it with `[ -r ]`, so a missing fragment is a no-op, not an error.

zsh gets **two** files, `~/.zshenv` and `~/.zprofile`, and both are required.
`.zshenv` runs on every invocation, but login shells also run `/etc/zprofile`, which
on macOS calls `path_helper` and rebuilds `PATH` — demoting anything `.zshenv` put in
front. The `.zprofile` hook re-sources the fragment afterwards. bash gets `~/.bashrc`
plus the first of `~/.bash_profile`, `~/.bash_login`, `~/.profile` that exists, since login
bash reads only that one; fish gets `config.fish`.

Each target rc file is auto-tracked if it isn't already and edited through the normal
snapshot path, so the user's tracked set grows — the output names what was tracked.
Install is idempotent and remove restores the file's original bytes. Until the user runs
`dfm path migrate`, the fragment is generated output: never tracked, never snapshotted,
regenerable at any time. After a migrate it is tracked and install stops regenerating it.

### Move the managed entries into the fragment

```sh
dfm path migrate --dry-run    # every step, writes nothing — read this first
dfm path migrate              # prompt, then move
dfm path migrate --yes        # move without prompting
```

Opt-in. It writes the fragment, installs the hooks, tracks the fragment, sets
`use_fragment = true` under `[path]` in `config.toml`, and only then strips the managed
blocks from the rc file through the normal snapshot path. The flag must land before the
strip: it is what stops `hook install` regenerating the fragment, so the other order
leaves a window where the blocks are gone from the rc file and dfm still thinks it can
rebuild from it. Failing before the strip just leaves the blocks in both places, which
is the pre-migrate state.

**After migrating, a bare `dfm path add` / `remove` / `list` / `fragment` targets the
fragment, not the rc file.** There is no per-command flag to remember. `--file <path>`
and `--shell` still win and still name a file. A user who never migrates sees exactly
the pre-migrate behaviour.

The fragment becoming tracked reverses what hook install says about it, and is
deliberate: post-migration it holds the user's configuration rather than a regenerable
copy of the rc file. That is also why `hook install` no longer regenerates it — doing so
from an rc file that no longer has the blocks would overwrite the user's PATH
configuration with an empty file.

Re-running `migrate` is a no-op that says so. To undo, in this order: `dfm path hook
remove`, then `dfm path add --file <rc>` or restore the pre-migrate snapshot, and only
then set `use_fragment = false`. Clearing the flag before the blocks are back and
running `hook install` overwrites the fragment from an empty rc file.

Migrated zsh blocks use the POSIX body, not `path=(...)`, because `env.sh` is shared
with sh and bash. Same dirs, same order, different text in the file.

Tracking runs the same secret scan as `dfm track`, so an rc file with something like
`export ACME_API_KEY=...` stops the install; `--force` tracks and installs anyway. An
unrecognised `--shell` is rejected rather than falling back to `~/.profile`.

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
- Do not pass `--show-secrets` to `dfm config show`; plain `config show` is redacted.
- Do not read or list `~/.lsm/` — it holds a private age key.
- Do not print tokens, or a libSQL URL beyond `scheme://host`.
- Do not run `dfm migrate`, `dfm prune`, or `dfm sync` speculatively. They are
  operational actions on real user data, not diagnostics.
- Do not use the user's real dotfiles as a scratch fixture.
