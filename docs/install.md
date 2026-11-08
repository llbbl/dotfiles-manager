# Install

`dfm` ships as a single static Go binary. Homebrew is the shortest path; everything else on this page is a fallback for when it does not fit.

## Homebrew (recommended)

```sh
brew install llbbl/tap/dfm
```

Upgrades come through the tap like any other formula:

```sh
brew update && brew upgrade dfm
```

The tap tracks releases automatically, but on a deliberate delay — the formula is bumped once a release is at least 24 hours old, so a brand-new version becomes available through Homebrew roughly a day after it ships. Use one of the methods below if you need it immediately.

## GitHub CLI

Auto-detects your platform and the latest release tag. Paste it as-is.

```sh
TAG=$(gh release view --repo llbbl/dotfiles-manager --json tagName -q .tagName)
VERSION=${TAG#v}
OS=$(uname -s | tr '[:upper:]' '[:lower:]')                  # darwin | linux
ARCH=$(uname -m | sed 's/aarch64/arm64/; s/x86_64/amd64/')   # arm64 | amd64
ASSET=dfm_${VERSION}_${OS}_${ARCH}.tar.gz

gh release download "$TAG" --repo llbbl/dotfiles-manager \
  -p "$ASSET" -p 'checksums.txt' --clobber

shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf "$ASSET"
mkdir -p ~/.local/bin && mv dfm ~/.local/bin/dfm
```

To pin a version, replace the first two lines with `TAG=v1.0.0; VERSION=${TAG#v}`.

## curl

Same thing without `gh`.

```sh
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/aarch64/arm64/; s/x86_64/amd64/')
TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
  https://github.com/llbbl/dotfiles-manager/releases/latest | sed 's|.*/||')
VERSION=${TAG#v}
ASSET=dfm_${VERSION}_${OS}_${ARCH}.tar.gz
BASE=https://github.com/llbbl/dotfiles-manager/releases/download/${TAG}

curl -fsSLO "$BASE/$ASSET"
curl -fsSLO "$BASE/checksums.txt"

shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf "$ASSET"
mkdir -p ~/.local/bin && mv dfm ~/.local/bin/dfm
```

To pin a version, set `TAG=v1.0.0` and drop the redirect-resolving `curl` line. On Linux, `sha256sum -c checksums.txt --ignore-missing` works in place of `shasum`.

## Manual download

Grab the archive for your platform from [releases](https://github.com/llbbl/dotfiles-manager/releases) and extract `dfm` onto your `PATH`.

| Platform | Asset |
| --- | --- |
| macOS ARM64 (Apple Silicon) | `dfm_<version>_darwin_arm64.tar.gz` |
| macOS x64 (Intel) | `dfm_<version>_darwin_amd64.tar.gz` |
| Linux ARM64 | `dfm_<version>_linux_arm64.tar.gz` |
| Linux x64 | `dfm_<version>_linux_amd64.tar.gz` |

Every release also publishes `checksums.txt`. Verify before running:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
```

## From source

Requires Go 1.26.10 and `just` — both pinned in [`mise.toml`](../mise.toml).

```sh
git clone https://github.com/llbbl/dotfiles-manager.git
cd dotfiles-manager
just install
just build-versioned
```

The binary lands at `./bin/dfm`. See [Development](./development.md) for working on `dfm` itself.

## Verifying the install

```sh
dfm version
```

If the command is not found, the install directory is not on your `PATH`. For the snippets above, add this to your shell rc:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

## First run

```sh
dfm init            # interactive setup: config, state store, backup repo
dfm --help          # full command surface
```

`dfm init` writes `~/.config/dotfiles/config.toml` and provisions the private backup repo. Flags and the non-interactive path are in [Commands](./commands.md#dfm-init). An [example config](../config.example.toml) ships in the source tree if you would rather write it by hand.

To keep the Turso auth token in [lsm](https://github.com/llbbl/lsm), install and
initialize lsm separately, then select `--secret-store lsm` during setup:

```sh
dfm init --turso-url libsql://your-database.turso.io --secret-store lsm --yes
```

Supply the token through `TURSO_AUTH_TOKEN` or `--turso-auth-token`. The default
lsm app and environment are `dfm` / `prod`; override them with `--lsm-app` and
`--lsm-env`. Interactive setup offers lsm by default when it is on PATH; EOF without an
answer keeps env storage. The selection also applies when `--turso` provisions a
database and mints its token. Setup
sends the token through stdin, saves only a `[state.secret]` reference, and removes
an existing plaintext token from the data env file after both saves succeed.
A failed lsm write stops setup without a plaintext fallback. An exported runtime
`TURSO_AUTH_TOKEN` overrides the reference. No `lsm exec` wrapper is needed.
See [init options](./commands.md#dfm-init) for preview and failure behavior.

To pin the AI model used by `dfm suggest` and `dfm ask`:

```toml
[ai.claude-code]
model = "sonnet"
```

## Upgrading

Homebrew: `brew update && brew upgrade dfm`. Otherwise repeat the download and verify flow with a newer tag — the binary is overwritten in place, and your config and state DB are untouched.

## Coexisting with a dev build

A released binary and a source build can sit side by side:

- `~/.local/bin/dfm` (or Homebrew's prefix) — resolved by `which dfm` from anywhere.
- `./bin/dfm` — built by `just build-versioned`, reachable only as an explicit path inside the repo.

They read the same config file and state DB, so both see the same tracked files unless you point them elsewhere with `--config`.

## Uninstalling

```sh
brew uninstall dfm                           # if installed via Homebrew
rm ~/.local/bin/dfm                          # otherwise
rm -rf ~/.config/dotfiles                    # config (optional)
rm -rf ~/.local/share/dotfiles               # state DB + snapshot blobs (optional)
```

Removing the state directory is destructive — pre-edit snapshots and the audit log live there. Keep it if you might reinstall.
