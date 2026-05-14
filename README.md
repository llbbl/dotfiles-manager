# dotfiles-manager

[![Auto Release](https://github.com/llbbl/dotfiles-manager/actions/workflows/auto-release.yml/badge.svg)](https://github.com/llbbl/dotfiles-manager/actions/workflows/auto-release.yml)
[![Latest Release](https://img.shields.io/github/v/release/llbbl/dotfiles-manager?sort=semver)](https://github.com/llbbl/dotfiles-manager/releases/latest)
[![License: FSL-1.1-MIT](https://img.shields.io/badge/license-FSL--1.1--MIT-blue)](./LICENSE.md)

A single distributable Go binary (`dfm`) that helps you manage, version, and improve your dotfiles. Every change is mirrored into a private GitHub backup repository with a full audit trail, and an AI coding agent (default: Claude Code) can propose improvements as reviewable patches.

Status: rapid iteration — version numbers track conventional-commit footers (see [Releases](./docs/development.md#releases)) and CLI flags / on-disk layout may still change between minor versions.

## Install

Grab a pre-built binary from the [Releases page](https://github.com/llbbl/dotfiles-manager/releases) — darwin and linux, arm64 and amd64. Full step-by-step (download, checksum, extract, install onto your `PATH`) is in [docs/install.md](./docs/install.md).

## First-run setup

After installing the binary, run `dfm init` once to set up the private backup repo and (optionally) the libSQL state store:

```sh
# Clone an existing private backup repo
dfm init --remote git@github.com:you/dotfiles-backup.git

# Or create a new private repo on your GitHub account in one step
dfm init --remote git@github.com:you/dotfiles-backup.git --create-remote

# Optionally provision a Turso libSQL DB for the state store
dfm init --turso
```

See [`dfm init --help`](./docs/commands.md) for all flags. State is stored at `~/.local/share/dotfiles/` and config at `~/.config/dotfiles/config.toml`; an [example config](./config.example.toml) ships in the repo.

## Build from source

```sh
just install              # tidy and download Go module deps
just build-versioned      # build ./bin/dfm with version info baked in
./bin/dfm version
./bin/dfm --help          # full command surface
```

## Documentation

- [Install](./docs/install.md) — install from GitHub Releases.
- [Development](./docs/development.md) — local setup, environment variables, testing, contribution workflow.
- [Commands](./docs/commands.md) — full `dfm` CLI reference.
- [Architecture](./docs/architecture.md) — what the moving parts are and how they fit together.
- [Changelog](./CHANGELOG.md) — release-by-release summary of changes.

## License

Licensed under the [Functional Source License, Version 1.1, MIT Future License](./LICENSE.md) (FSL-1.1-MIT). All non-Competing Use is permitted today; the Software additionally becomes available under the MIT license on the second anniversary of each release.
