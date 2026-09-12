# dotfiles-manager

[![Auto Release](https://github.com/llbbl/dotfiles-manager/actions/workflows/auto-release.yml/badge.svg)](https://github.com/llbbl/dotfiles-manager/actions/workflows/auto-release.yml)
[![Latest Release](https://img.shields.io/github/v/release/llbbl/dotfiles-manager?sort=semver)](https://github.com/llbbl/dotfiles-manager/releases/latest)
[![License: FSL-1.1-MIT](https://img.shields.io/badge/license-FSL--1.1--MIT-blue)](./LICENSE.md)

A single distributable Go binary (`dfm`) that helps you manage, version, and improve your dotfiles. Every change is mirrored into a private GitHub backup repository with a full audit trail, and an AI coding agent (default: Claude Code) can propose improvements as reviewable patches.

Status: rapid iteration — version numbers track conventional-commit footers (see [Releases](./docs/development.md#releases)) and CLI flags / on-disk layout may still change between minor versions.

## Install

```sh
brew install llbbl/tap/dfm
```

Not a Homebrew user? `gh`, `curl`, and manual-download recipes are in [docs/install.md](./docs/install.md).

## First run

```sh
dfm init            # config, state store, and private backup repo
dfm track ~/.zshrc  # start managing a file
dfm status          # what changed since last sync
dfm sync            # mirror tracked files to the backup repo
```

`dfm init` clones an existing private backup repo, or creates one for you:

```sh
dfm init --remote git@github.com:you/dotfiles-backup.git
dfm init --remote git@github.com:you/dotfiles-backup.git --create-remote
dfm init --turso    # optionally provision a Turso libSQL state store
```

Config lands at `~/.config/dotfiles/config.toml`, state at `~/.local/share/dotfiles/`. An [example config](./config.example.toml) ships in the repo. Full flag reference: [docs/commands.md](./docs/commands.md).

## Build from source

```sh
just install              # tidy and download Go module deps
just build-versioned      # build ./bin/dfm with version info baked in
./bin/dfm version
```

## Claude Code plugin

A Claude Code plugin ships from this repository so an AI assistant driving `dfm` knows the rules that are not in the flag reference — that managed rc-file blocks are generated and must not be hand-edited, that switching the state backend does not migrate rows, and that every mutation leaves a snapshot to roll back to.

```
/plugin marketplace add llbbl/dotfiles-manager
/plugin install dfm@llbbl-dotfiles-manager
```

That installs the `/dfm:manage` skill. Source is in [`skills/manage/SKILL.md`](./skills/manage/SKILL.md).

## Documentation

- [Install](./docs/install.md) — install from GitHub Releases.
- [Development](./docs/development.md) — local setup, environment variables, testing, contribution workflow.
- [Commands](./docs/commands.md) — full `dfm` CLI reference.
- [Architecture](./docs/architecture.md) — what the moving parts are and how they fit together.
- [Changelog](./CHANGELOG.md) — release-by-release summary of changes.

## License

Licensed under the [Functional Source License, Version 1.1, MIT Future License](./LICENSE.md) (FSL-1.1-MIT). All non-Competing Use is permitted today; the Software additionally becomes available under the MIT license on the second anniversary of each release.
