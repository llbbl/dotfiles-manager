# dotfiles-manager

A single distributable Go binary (`dotfiles`) that helps you manage, version, and improve your dotfiles. It tracks every change in a private GitHub repository so there is a full audit log of edits, AI suggestions, and who or what changed what.

Status: pre-alpha, milestone M0

## Quickstart

```sh
just install           # tidy and download Go module deps
just dev version       # run the CLI from source
just build-versioned   # build ./bin/dotfiles with version info baked in
./bin/dotfiles version
```

## Development

Run `just --list` to see all available recipes (build, test, lint, migrations, version bumps, etc.).

Requirements are pinned in `mise.toml` (Go 1.25.7, just, goose). Activate them with `mise install`.

## License

Licensed under the [Functional Source License, Version 1.1, MIT Future License](./LICENSE.md) (FSL-1.1-MIT). All non-Competing Use is permitted today; the Software additionally becomes available under the MIT license on the second anniversary of each release.
