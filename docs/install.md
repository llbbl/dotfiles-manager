# Install

`dfm` ships as a single static Go binary. The fastest path is to grab a pre-built release from GitHub. If you want to build from source instead, see [Development](./development.md).

## Supported platforms

Releases publish four tarballs and a `checksums.txt`:

| OS    | Architecture  | Tarball                              |
| ----- | ------------- | ------------------------------------ |
| macOS | Apple Silicon | `dfm_<version>_darwin_arm64.tar.gz`  |
| macOS | Intel         | `dfm_<version>_darwin_amd64.tar.gz`  |
| Linux | arm64         | `dfm_<version>_linux_arm64.tar.gz`   |
| Linux | x86_64        | `dfm_<version>_linux_amd64.tar.gz`   |

The install snippets below auto-detect your platform via `uname` — no manual selection needed.

## Install (GitHub CLI)

The smoothest path uses [`gh`](https://cli.github.com/). It handles authentication and prefix-matched downloads. The block below auto-detects your platform and the latest release tag — paste it as-is.

```sh
# Latest release tag (e.g. v1.0.0)
TAG=$(gh release view --repo llbbl/dotfiles-manager --json tagName -q .tagName)
VERSION=${TAG#v}

# Auto-detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')                            # darwin | linux
ARCH=$(uname -m | sed 's/aarch64/arm64/; s/x86_64/amd64/')             # arm64 | amd64
ASSET=dfm_${VERSION}_${OS}_${ARCH}.tar.gz

gh release download "$TAG" \
  --repo llbbl/dotfiles-manager \
  -p "$ASSET" \
  -p 'checksums.txt' \
  --clobber

# Verify the download against the published checksum
shasum -a 256 -c checksums.txt --ignore-missing
# expected: <asset>: OK

# Extract and install onto your PATH
tar -xzf "$ASSET"
mkdir -p ~/.local/bin
mv dfm ~/.local/bin/dfm

# Confirm
which dfm        # expected: /Users/<you>/.local/bin/dfm
dfm version
```

To pin a specific version instead of the latest, replace the first two lines with `TAG=v1.0.0; VERSION=${TAG#v}`.

If `~/.local/bin` is not on your `PATH`, add it to your shell rc:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

## Install (curl, no gh)

```sh
# Auto-detect platform (same as above)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/aarch64/arm64/; s/x86_64/amd64/')

# Resolve latest tag without gh (follow the redirect from /releases/latest)
TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
  https://github.com/llbbl/dotfiles-manager/releases/latest | sed 's|.*/||')
VERSION=${TAG#v}

ASSET=dfm_${VERSION}_${OS}_${ARCH}.tar.gz
BASE=https://github.com/llbbl/dotfiles-manager/releases/download/${TAG}

curl -fsSLO "$BASE/$ASSET"
curl -fsSLO "$BASE/checksums.txt"

shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf "$ASSET"
mkdir -p ~/.local/bin
mv dfm ~/.local/bin/dfm
dfm version
```

To pin a version, set `TAG=v1.0.0` and drop the redirect-resolving `curl` line.

Linux: `shasum` is usually available; `sha256sum -c checksums.txt --ignore-missing` works too.

## Coexisting with a dev build

If you've cloned the repo for development, you can keep both binaries side by side:

- `~/.local/bin/dfm` — released binary, resolved by `which dfm` from any directory.
- `./bin/dfm` — locally built via `just build-versioned`. Only reachable as an explicit path from inside the repo.

This is the standard "install for everyday use, build for branch testing" split. The two don't share state — they read the same config file and state DB, so a release and a dev build will see the same tracked files unless you point them at different paths.

## First run

After install:

```sh
dfm --help          # full command surface
dfm version         # confirm the version you installed

# Create a config file (recommended). Today the simplest way is to copy
# the example and edit. An interactive `dfm init` wizard is in flight.
mkdir -p ~/.config/dotfiles
# Then edit ~/.config/dotfiles/config.toml — see config.example.toml in
# the source tree for the full set of keys.
```

For runtime config (state backend, AI model, log level) see [Commands](./commands.md) and [Architecture](./architecture.md). To pin the AI model used by `dfm suggest` and `dfm ask`:

```toml
[ai.claude-code]
model = "sonnet"
```

## Upgrading

Repeat the download + verify + extract + `mv` flow with a newer version tag. The binary at `~/.local/bin/dfm` is overwritten in place; your config and state DB are not touched.

To see what changed between versions, check the GitHub Releases page or `git log --oneline v<old>..v<new>` if you have the source checked out.

## Uninstalling

```sh
rm ~/.local/bin/dfm                          # binary
rm -rf ~/.config/dotfiles                    # config (optional)
rm -rf ~/.local/share/dotfiles               # state DB + snapshot blobs (optional)
```

Removing the state directory is destructive — pre-edit snapshots and the audit log live there. Keep it if you might reinstall.
