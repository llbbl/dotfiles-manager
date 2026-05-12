# Development

## Toolchain

Required tool versions are pinned in [`mise.toml`](../mise.toml):

- Go 1.25.7
- just 1.46.0

Install [mise](https://mise.jdx.dev/) and run:

```sh
mise install
```

If you don't use mise, install Go and just manually at the pinned versions.

## Common just recipes

`just --list` shows every recipe with a short description. The ones you'll reach for most:

| Recipe | What it does |
|---|---|
| `just install` | `go mod tidy` + `go mod download` |
| `just dev <args>` | Run the CLI from source with the given arguments |
| `just build` | Build `./bin/dfm` |
| `just build-versioned` | Build with the current git tag (or `dev`) injected via ldflags |
| `just test` | `go test ./...` |
| `just test-race` | `go test -race -cover ./...` |
| `just lint` | golangci-lint if installed, else `go vet ./...` |
| `just lint-fix` | gofmt + `golangci-lint --fix` |
| `just check` | lint + test |
| `just ci` | install (frozen lockfile) + lint + test-race |
| `just migrate-status` / `migrate-up` / `migrate-down` | Goose migrations against the configured state store |
| `just migrate-new <name>` | Scaffold a new goose migration file |
| `just bump-patch` / `bump-minor` / `bump-major` | Tag a new release locally |

## Environment variables

Everything is read from process environment, so it composes with any secret-injection tool you like (plain `.env`, direnv, etc.). `just` auto-loads `.env` via `set dotenv-load := true`.

| Variable | Purpose | Default |
|---|---|---|
| `TURSO_DATABASE_URL` | Remote libSQL/Turso URL for the state store. Overrides `[state].url` in `config.toml` when set. Unset = use the embedded file default. | unset |
| `TURSO_AUTH_TOKEN` | Auth token for the remote Turso DB. Overrides `[state].auth_token` when set. | unset |
| `DOTFILES_LOG_BACKEND` | Audit-log destination: `both` (JSONL + libSQL), `jsonl`, `db`, or `none`. | `both` |
| `DOTFILES_LOG_LEVEL` | Debug logger level: `debug`, `info`, `warn`, `error`, or `off`. Distinct from the audit log. | `off` |
| `DOTFILES_LOG_DEST` | Debug logger destination: `stderr`, `stdout`, or `file:/absolute/path`. | `stderr` |
| `DOTFILES_LOG_FORMAT` | Debug logger format: `text` or `json`. | `text` |

Two distinct logging concerns to keep in mind:

- **Audit log** records user-visible events (`track`, `sync`, `apply`, …) to JSONL + libSQL. Always on; backend selectable. See [Architecture](./architecture.md).
- **Debug log** records internal flow traces for developers. Off by default; opt in with `DOTFILES_LOG_LEVEL`.

To enable rich debug output during local work:

```sh
DOTFILES_LOG_LEVEL=debug DOTFILES_LOG_FORMAT=json just dev list
```

Or write to a file and tail it in another terminal:

```sh
DOTFILES_LOG_LEVEL=debug \
  DOTFILES_LOG_FORMAT=json \
  DOTFILES_LOG_DEST=file:/tmp/dfm-dlog.jsonl \
  just dev sync
```

## State store

By default, state lives in an embedded libSQL file at `~/.local/share/dotfiles/state.db`. Migrations live in [`internal/store/migrations/`](../internal/store/migrations) and run automatically on every `dfm migrate up`.

To use a remote Turso database instead:

```sh
turso db create dotfiles-state
turso db show dotfiles-state --url      # set as TURSO_DATABASE_URL
turso db tokens create dotfiles-state   # set as TURSO_AUTH_TOKEN
```

Drop those two values into your env (or `.env`) and the binary picks them up automatically. No code or config change required.

## Testing

```sh
just test            # fast
just test-race       # with the race detector + coverage
go test -count=1 ./...   # if you need to bypass the test cache
```

Tests never touch the user's real Turso database or real GitHub repos. State-store tests run against an embedded-file store under `t.TempDir()`. VCS tests run against a local bare repo under `t.TempDir()`.

## Project conventions

- **Pre-release**: direct commits to `main` are fine. Once the project cuts v0.1 or opens to external contributors, the workflow shifts to feature branches + pull requests.
- **No third-party Go dependencies are added casually.** Stdlib first; current direct deps are limited to cobra, BurntSushi/toml, tursogo, and pressly/goose. Any addition needs a clear justification.
- **Security**: never log secrets, API tokens, Turso URLs beyond `scheme://host`, prompt or response bodies, or file contents. Paths and display paths are okay (they already appear in the audit log).

## Filing issues

Use the project's [GitHub issues](https://github.com/llbbl/dotfiles-manager/issues). Each issue is the canonical public record of a piece of work; commits that close one reference it with `Closes #N`.
