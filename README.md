# dotfiles-manager

A single distributable Go binary (`dotfiles`) that helps you manage, version, and improve your dotfiles. Every change is mirrored into a private GitHub backup repository with a full audit trail, and an AI coding agent (default: Claude Code) can propose improvements as reviewable patches.

Status: pre-alpha — APIs and CLI flags may change without notice.

## Quickstart

```sh
just install              # tidy and download Go module deps
just build-versioned      # build ./bin/dotfiles with version info baked in
./bin/dotfiles version
./bin/dotfiles --help     # full command surface
```

## Documentation

- [Development](./docs/development.md) — local setup, environment variables, testing, contribution workflow.
- [Commands](./docs/commands.md) — full `dotfiles` CLI reference.
- [Architecture](./docs/architecture.md) — what the moving parts are and how they fit together.

## License

Licensed under the [Functional Source License, Version 1.1, MIT Future License](./LICENSE.md) (FSL-1.1-MIT). All non-Competing Use is permitted today; the Software additionally becomes available under the MIT license on the second anniversary of each release.
