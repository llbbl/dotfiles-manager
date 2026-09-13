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

#### Aliases do not work in bash scripts

An alias is an interactive-shell convenience. Non-interactive bash — every `bash script.sh`, every `#!/bin/bash` file, every `bash -c` — does not expand aliases at all unless the script itself runs `shopt -s expand_aliases` first, and even then the alias has to be defined before the line that uses it is *parsed*, which rules out defining and using one in the same block. zsh expands aliases in scripts, so something that works there will silently do nothing under bash.

If you need it to work in a script, an alias is the wrong tool. Use a shell function, or a real executable on `PATH`:

```sh
# works everywhere, including non-interactive bash
gc() { git commit -m "$1"; }
```

Keep aliases for what you type at a prompt.

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

The markers, the id, and the block bounds are identical everywhere; only the body between them varies by shell, picked automatically from `--shell`, from the rc filename behind `--file`, or from `$SHELL`.

bash, sh, and `.profile` get a POSIX body — `--shell profile` targets `/bin/sh`, which is bash 3.2 on macOS. It rebuilds `PATH` without the managed dir and then re-inserts it at the requested end, so a dir already present at lower precedence is promoted and duplicates collapse:

```
# dfm:path:<id8> >>> direction=prepend dirs=/a:/b:/c
for __dfm_d in /a /b /c; do
  __dfm_rest=$PATH; __dfm_new=
  while [ -n "$__dfm_rest" ]; do
    __dfm_p=${__dfm_rest%%:*}
    case $__dfm_rest in
      *:*) __dfm_rest=${__dfm_rest#*:} ;;
      *) __dfm_rest= ;;
    esac
    [ "$__dfm_p" = "$__dfm_d" ] || __dfm_new=${__dfm_new:+$__dfm_new:}$__dfm_p
  done
  PATH="$__dfm_d${__dfm_new:+:$__dfm_new}"
done
unset __dfm_d __dfm_p __dfm_new __dfm_rest
export PATH
# dfm:path:<id8> <<<
```

`PATH` is split by hand rather than by setting `IFS`: zsh does not word-split an unquoted parameter, and restoring a previously-unset `IFS` leaves it empty rather than unset, which would kill word splitting for the rest of the sourcing shell.

zsh gets its native `path` array, which removes and re-inserts in one step — so a dir already on `PATH` is promoted to the front (or moved to the back with `--append`) and any duplicates collapse:

```
# dfm:path:<id8> >>> direction=prepend dirs=/a:/b:/c
# Assumes zsh's default tie between $path and $PATH, so no export is needed.
for __dfm_d in /a /b /c; do
  path=($__dfm_d ${path:#$__dfm_d})
done
unset __dfm_d
# dfm:path:<id8> <<<
```

fish delegates to `fish_add_path`, fish's own implementation of this feature, which is idempotent on its own. `--path` keeps the change on `$PATH` in the sourcing shell rather than the universal `fish_user_paths`, and `--move` promotes a dir that is already present. This requires **fish 3.2 or newer** (2021; Ubuntu 22.04 ships 3.3, 24.04 ships 3.7):

```
# dfm:path:<id8> >>> direction=prepend dirs=/a:/b:/c
for __dfm_d in /a /b /c
    fish_add_path --path --move $__dfm_d
end
set -e __dfm_d
# dfm:path:<id8> <<<
```

A few consequences of using each shell's own machinery are worth knowing:

- **fish does not collapse a duplicate that is already on `PATH`.** `fish_add_path --move` promotes the entry it manages, but a second copy of the same directory placed elsewhere by something else stays. The zsh and POSIX bodies remove it. Measured against fish 3.7.
- **fish ignores a directory that does not exist on disk.** That is `fish_add_path`'s documented behaviour, not dfm's choice. The bash and zsh bodies add the dir regardless, so an rc file synced to a machine before the directory is created behaves differently under fish. fish also normalises the token (`.`, `..`, trailing slashes, relative paths), so what lands on `$PATH` can differ textually from the `dirs=` list in the marker.
- **Empty `PATH` components are handled differently per shell.** An empty component means the current directory. The zsh body drops every one of them: `/usr/bin::/bin` becomes `/usr/bin:/bin`. The bash body drops a leading or trailing one but keeps an interior one, so that same input survives as `/usr/bin::/bin`. Neither body ever introduces one.

`<id8>` is the first eight hex characters of `sha256("<direction>:<dirs>")` and rotates whenever the dir list changes — the marker is data, not an identifier you depend on. There's at most one managed block per direction per rc file (`prepend` and `append` are independent entries); a second managed block in the same direction is treated as corruption and refused (see "Corruption guard" below).

Blocks are found by their markers alone, never by their body, so a block written for one shell is replaced in place rather than duplicated when the body shape changes. An existing block is only rewritten when its dir list changes, though, so a block written by an older dfm keeps its old body until something edits it — `dfm path remove <dir>` followed by `dfm path add <dir>` regenerates it with the current one.

### `dfm path add <dir>`

Add `<dir>` to the dfm-managed PATH entry. The first call creates the block; subsequent calls splice the new dir into the existing block and rotate the marker id.

```sh
dfm path add ~/.local/bin
dfm path add ~/.cargo/bin
dfm path add /opt/homebrew/sbin --append
```

Flags:

- `--shell <bash|zsh|fish|profile>` — pick which rc file to target (defaults to `$SHELL`).
- `--file <path>` — explicit rc file. Mutually exclusive with `--shell`.
- `--append` — write into the append-direction entry (default: prepend).
- `--force` — bypass the secrets pre-flight scan. Does NOT bypass dedup — adding a dir that's already on the managed entry still exits 4.

Exit 4 if the dir is already on the managed entry in the same direction (cross-spelling equivalence: `~/x` and `$HOME/x` are the same dir). Exit 3 on secrets findings (suppressible with `--force`).

### `dfm path remove <dir>`

Remove `<dir>` from the dfm-managed PATH entry. If the entry has multiple dirs the block shrinks; if `<dir>` was the only dir the entire block is dropped (no empty stub left behind).

Flags:

- `--shell <bash|zsh|fish|profile>` — pick which rc file to target.
- `--file <path>` — explicit rc file. Mutually exclusive with `--shell`.

Exit 4 if `<dir>` is not on any dfm-managed entry in the target file.

### `dfm path list`

List the dirs on each dfm-managed PATH entry in the target rc file. Default output is tab-separated rows of `DIR / DIRECTION / MARKER_ID`; `--json` emits a flat array of `{dir, direction, marker_id}` objects.

`list` only enumerates dfm-managed entries — installer blocks (pnpm, nvm, mise, etc.) are intentionally invisible to this command. Use your shell directly (`echo $PATH | tr ':' '\n'`) to see your full effective `PATH`.

Flags:

- `--shell <bash|zsh|fish|profile>` — pick which rc file to target.
- `--file <path>` — explicit rc file. Mutually exclusive with `--shell`.
- `--json` — JSON output.

### `dfm path fragment`

Print the dfm-managed PATH entries of the target rc file re-rendered as a standalone, sourceable fragment. Read-only — nothing is written, and no rc file is touched.

```sh
dfm path fragment                  # the target's own family
dfm path fragment --shell zsh      # POSIX syntax, for sh, bash and zsh
dfm path fragment --fish           # fish syntax
```

There are two syntax families because fish cannot source POSIX syntax. `env.sh` serves sh, bash **and** zsh — one file has to cover all three, so zsh gets the POSIX body rather than its native `path=()` one; `env.fish` serves fish. Their canonical locations are `$XDG_CONFIG_HOME/dotfiles/env.sh` and `$XDG_CONFIG_HOME/dotfiles/env.fish`, named in each fragment's header.

An rc file with no dfm-managed entries is not an error: you get the header and nothing else.

Which rc file the entries are read from and which syntax they are rendered in are separate choices. The target's own family is the default — `--shell fish` prints the fish fragment, and `--shell zsh` prints the POSIX one because `env.sh` is what zsh ends up sourcing. `--fish` asks for the fish fragment from whatever entries the target holds.

Flags:

- `--shell <bash|zsh|fish|profile>` — pick which rc file to read entries from.
- `--file <path>` — explicit rc file. Mutually exclusive with `--shell`.
- `--fish` — render the fish fragment regardless of the target's own family.

### `dfm path hook install`

Write the generated fragment to `$XDG_CONFIG_HOME/dotfiles/` and install a one-time hook into the shell's startup files so every new shell sources it.

```sh
dfm path hook install                    # follows $SHELL
dfm path hook install --shell zsh
dfm path hook install --dry-run          # name every file that would change
```

The hook is fixed, three lines, and marker-delimited:

```sh
# >>> dfm:env >>>
__dfm_env="${XDG_CONFIG_HOME:-$HOME/.config}/dotfiles/env.sh"
[ -r "$__dfm_env" ] && . "$__dfm_env"
unset __dfm_env
# <<< dfm:env <<<
```

The fragment path is resolved at shell startup, not baked in at install time, so the rc file stays portable to any machine your backup repo reaches. The `[ -r ]` guard (`test -r` in fish) makes a missing fragment a no-op rather than an error: the shell starts normally with `PATH` untouched.

Which files each shell gets:

| shell | files |
|---|---|
| zsh | `~/.zshenv` **and** `~/.zprofile` |
| bash | `~/.bashrc`, plus the first of `~/.bash_profile`, `~/.bash_login`, `~/.profile` that exists |
| fish | `~/.config/fish/config.fish` |
| profile | `~/.profile` |

**zsh needs two files.** `~/.zshenv` runs on every zsh invocation, which covers non-login shells and scripts. Login shells also run `/etc/zprofile`, which on macOS calls `path_helper` — that rebuilds `PATH` from `/etc/paths` and `/etc/paths.d` and demotes anything `.zshenv` prepended to the back. The `~/.zprofile` hook re-sources the fragment after `path_helper` has run, which is the only way a managed dir stays in front inside `zsh -l`. Both hooks firing in one shell is harmless: the fragment's blocks are idempotent, so a managed dir appears exactly once.

**bash picks one login file.** Login bash reads the first of `~/.bash_profile`, `~/.bash_login`, `~/.profile` that exists and stops there, so the hook follows that same order. Writing to a file a shadowing one precedes would install a hook bash never sources. If none of the three exists, `~/.profile` is created.

Each rc file the hook needs is tracked automatically if it isn't already, then edited through the normal snapshot path — so the change is in the audit log and recoverable like any other dfm edit. Newly tracked files are named in the output. The file is created (mode 0644) if it doesn't exist.

Tracking runs the same secret scan `dfm track` does, and an rc file holding something that looks like a credential — `export ACME_API_KEY=…` is enough — stops the install. Pass `--force` to track and install anyway. Nothing is tracked and no hook is written when the scan refuses, though the regenerated fragment may already be on disk; it is harmless there, since nothing sources it until a hook exists.

Installing twice is a no-op: the files come out byte-identical and the command says so. Until you run `dfm path migrate`, the fragment is generated output, not a dotfile — it is never tracked, never snapshotted, and never mirrored to the backup repo, because it is regenerable from the rc file at any time. After a migrate that reverses: the fragment holds your configuration, it is tracked, and `hook install` stops regenerating it and says so.

Flags:

- `--shell <bash|zsh|fish|profile>` — which shell's file set to operate on. Defaults to `$SHELL`.
- `--dry-run` — print what would change, including which files would be tracked, and touch nothing.
- `--force` — track an rc file even if it trips the secret scan.

An unrecognised `--shell` is rejected rather than falling back to `~/.profile`, so a typo cannot create a startup file for a shell you did not name.

### `dfm path hook remove`

Strip the `dfm:env` hook block from the same file set.

```sh
dfm path hook remove --shell zsh
dfm path hook remove --dry-run
```

The rest of the file — including its trailing newline structure — is left byte-identical, so a remove restores exactly what was there before the install. Removing when no hook is present is not an error. The fragment file is left in place; nothing sources it once the hooks are gone.

### `dfm path migrate`

Move the dfm-managed PATH blocks out of your rc file and make the generated fragment their only home. Opt-in: until you run this, nothing about `dfm path` changes.

```sh
dfm path migrate --dry-run         # print every step, write nothing
dfm path migrate                   # prompt, then move
dfm path migrate --yes             # move without prompting
```

The steps run in this order, so no interruption can leave your entries with only one damaged copy on disk:

1. Render the fragment from the rc file's managed blocks and write it to `$XDG_CONFIG_HOME/dotfiles/`.
2. Install the `dfm:env` hooks, exactly as `dfm path hook install` does.
3. Track the fragment.
4. Set `use_fragment = true` under `[path]` in `config.toml`. If the file still held `[state].auth_token`, it moves to `$XDG_DATA_HOME/dotfiles/.env` (mode 0600) and the move is reported.
5. Strip the managed blocks from the rc file, through the normal snapshot path.

The last two are in that order on purpose. The flag is what tells `hook install` to stop regenerating the fragment, so stripping first would open a window — a failed config write, or a Ctrl-C — where the blocks are gone from the rc file while dfm still believes it can rebuild the fragment from it. Failing the other way round is harmless: the blocks are simply still in both places, which is exactly the state you were in before running migrate.

**The fragment becomes a tracked dotfile.** That reverses what `hook install` says about it, and deliberately: before a migrate the fragment is a regenerable copy of the rc file, after one it holds the configuration itself, so it is snapshotted and mirrored to the backup repo like anything else you track. `dfm path hook install` stops regenerating it for the same reason — regenerating from an rc file that no longer has the blocks would overwrite your PATH configuration with an empty file.

**After migrating, `dfm path add` / `remove` / `list` / `fragment` target the fragment** with no flag to remember. `--file <path>` still wins and still targets exactly the file you name; so does `--shell`, which names a shell's rc file. Everything outside the managed blocks is left in the rc file byte-identical, including the blank separator line an add wrote in front of a block.

Running `migrate` when `use_fragment` is already on says so and changes nothing.

**zsh entries render as POSIX in the fragment.** `env.sh` is shared by sh, bash and zsh, so a migrated zsh block uses the POSIX body rather than zsh's native `path=(...)`. The directories and their order are unchanged, and the POSIX body promotes an already-present dir and collapses duplicates the same way — but the text of the block in your file does change, which is visible if you diff it.

`--dry-run` writes no fragment, no hook and no config, and edits no rc file. It does open the state database, creating and migrating it if this is a fresh install, because it resolves tracked files to report what would be tracked.

To go back, in this order: `dfm path hook remove` to strip the hooks, then move the blocks back with `dfm path add --file <rc>` (or restore the pre-migrate snapshot — `dfm backups` lists it, `dfm restore <snapshot-id>` applies it), and only then set `use_fragment = false` in `config.toml`. Clearing the flag first and running `hook install` before the blocks are back walks into the regeneration path and overwrites the fragment from an rc file that has nothing in it.

Flags:

- `--shell <bash|zsh|fish|profile>` — which shell's rc file to migrate. Defaults to `$SHELL`.
- `--dry-run` — print every step, including the config write, and write nothing.
- `--yes`, `-y` — migrate without prompting. Required when there is no TTY to prompt on.
- `--force` — track a file even if it trips the secret scan.

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
- `--file <path>` — explicit rc file. Mutually exclusive with `--shell`.
- `--dry-run` — print the proposal and exit without writing.
- `-y`, `--yes` — apply without prompting. Required when stdin is non-interactive (a piped or redirected shell errors out without `--yes` or `--dry-run`).
- `--json` — emit a JSON report instead of human-readable text. JSON mode keeps stdout clean even when the interactive prompt fires (prompt text goes to stderr).

`--dry-run` wins if both `--dry-run` and `--yes` are set. The command refuses if the target rc file already has a dfm-managed prepend entry — clean up with `dfm path remove` first, or add dirs to the existing entry with `dfm path add`.

### Coexistence with installer blocks

dfm's managed PATH block matches the shape pnpm, nvm, mise, and rustup all use — a substring guard so re-sourcing the rc file doesn't re-prepend. Installer blocks are detected and skipped during `dfm path` operations: they aren't enumerated by `dfm path list`, aren't disturbed by `dfm path add`/`remove`/`import`, and stay byte-identical across dfm mutations. You can let installers continue to manage their own dirs; dfm only owns the dirs you explicitly `path add` or import.

### Re-source idempotency

The managed block is built so sourcing your rc file repeatedly never grows `PATH`. Each shell gets there its own way — a `case` guard on bash, a remove-then-insert `path` assignment on zsh, `fish_add_path` on fish — but the result is the same. You can verify this on your own shell:

```sh
echo $PATH | tr ':' '\n' | sort | uniq -c | sort -rn | head
```

Any line with a count > 1 is a duplicate. After moving dirs into a dfm-managed entry and re-sourcing, the managed dirs each appear exactly once regardless of how many times the rc file has been sourced.

### Zsh `typeset -U path` pairing

dfm's zsh block does not set `typeset -U path` and won't: that flag is persistent, so it would silently change every later `PATH` assignment in your session, not just the managed block. The block dedupes only the dirs it owns.

You can still add `typeset -U path PATH` near the top of `~/.zshrc` yourself if you want it — it tells zsh to dedupe `path` on every assignment, which cleans up non-dfm-managed lines (legacy installer blocks, hand-edited exports) that have no guard of their own. The two mechanisms are independent and stack cleanly.

## Backup repo + sync

### `dfm init`

First-run setup. Probes `[repo].remote` with `git ls-remote`:

`config.toml` never holds `[state].auth_token`. Any command that rewrites it moves a token already in the file to `$XDG_DATA_HOME/dotfiles/.env` (mode 0600) and says so.

A token that dfm only *reads* from `$TURSO_AUTH_TOKEN` at runtime is never written anywhere. A token you hand it outright — `--turso-auth-token`, or `$TURSO_AUTH_TOKEN` at the moment you run `dfm init --turso` — is written to that same `.env` file, because otherwise it would be discarded and the setup would not work on your next command.

**dfm does not read that `.env` file for you.** It is written for your shell to source; `dfm` only ever reads `$TURSO_AUTH_TOKEN` from the process environment. So after a token is moved there, export it from your shell or remote state will stop authenticating. `[runtime].dotenv` searches `$XDG_CONFIG_HOME/dotfiles/.env` and `./.env`, which is a different path, and is off by default.


- Remote reachable and non-empty → clone it into `[repo].local`.
- Remote reachable but empty → with `--create-remote` (or interactive confirm), run `gh repo create --private`, push an initial commit.
- Remote unreachable → exit 2 with the underlying git error.

Flags:

- `--remote <url>` — override `[repo].remote`.
- `--create-remote` — accept the gh-create flow non-interactively.
- `--yes` — non-interactive: accept all defaults.
- `--print` — render the would-be config to stdout and write nothing.
- `--force` — overwrite an existing config without edit-mode pre-fills.
- `--state <local|turso>` — pick the state-store branch of the wizard.
- `--turso` — provision a Turso libSQL remote DB.
- `--turso-db-name <name>` — DB to create or reuse (default `dotfiles-state`).
- `--turso-url <url>` — bake in an existing `libsql://` URL and skip provisioning.
- `--turso-auth-token <token>` — use an existing auth token. When the config is written it goes to `$XDG_DATA_HOME/dotfiles/.env` (mode 0600), never to `config.toml`. Under `--print` nothing is written at all, and the rendered config omits the token.
- `--ai-model <name>` / `--ai-bin <path>` — override the AI chapter.

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
- `--suggestion <id>` — show the full lifecycle of a single AI suggestion (suggest → apply or reject), in ascending timestamp order. Any unambiguous id prefix works; an ambiguous prefix lists the candidates and exits 7, and a prefix matching no suggestion simply yields no rows.
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

Any unambiguous id prefix works, including the shortened ids `dfm suggestions` prints. An ambiguous prefix lists the matching ids and exits 7.

If anything fails after the snapshot is taken, the source file is left untouched, the suggestion stays `pending`, and the error message includes the snapshot id with a `dfm restore` hint.

### `dfm reject <suggestion-id>`

Mark a pending suggestion as rejected without applying it. Accepts any unambiguous id prefix.

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

Any unambiguous id prefix works, including the shortened ids `dfm backups` prints. An ambiguous prefix lists the matching snapshots and exits 7.

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
