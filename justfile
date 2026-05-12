# dotfiles-manager - justfile
# Run `just` or `just --list` to see available commands

set dotenv-load := true

BIN := "dfm"
PKG := "./cmd/dfm"

# Default recipe: show help
default:
    @just --list --unsorted

# Tidy and download deps
install:
    go mod tidy
    go mod download

# Verify modules (for CI)
install-frozen:
    go mod download
    go mod verify

# Run the CLI locally
dev *ARGS:
    go run {{ PKG }} {{ ARGS }}

# Run all tests
test *ARGS:
    go test ./... {{ ARGS }}

# Run tests with race detector and coverage
test-race:
    go test -race -cover ./...

# Vet + staticcheck-style checks (uses golangci-lint if installed, else go vet)
lint:
    #!/bin/sh
    if command -v golangci-lint >/dev/null 2>&1; then
        golangci-lint run ./...
    else
        echo "golangci-lint not found, falling back to 'go vet'"
        go vet ./...
    fi

# Auto-fix lint issues and format code
lint-fix:
    #!/bin/sh
    gofmt -w .
    if command -v golangci-lint >/dev/null 2>&1; then
        golangci-lint run --fix ./...
    fi

# Alias for lint-fix
format: lint-fix

# Build a local binary into ./bin
build:
    mkdir -p ./bin
    go build -o ./bin/{{ BIN }} {{ PKG }}

# Build with version info baked in (uses latest git tag, else 'dev')
build-versioned:
    #!/bin/sh
    set -e
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
    mkdir -p ./bin
    go build -ldflags "-X main.version=$VERSION" -o ./bin/{{ BIN }} {{ PKG }}
    echo "built ./bin/{{ BIN }} ($VERSION)"

# Install the binary to $GOBIN / $GOPATH/bin
install-bin:
    go install {{ PKG }}

# Remove build artifacts
clean:
    rm -rf ./bin ./dist

# Run all checks (lint + test)
check: lint test

# CI workflow
ci: install-frozen lint test-race

# ============================================================================
# Migrations (goose)
# ============================================================================

# Show migration status against the local state db
migrate-status:
    go run {{ PKG }} migrate status

# Apply all pending migrations
migrate-up:
    go run {{ PKG }} migrate up

# Roll back the last migration
migrate-down:
    go run {{ PKG }} migrate down

# Create a new goose migration file
migrate-new NAME:
    #!/bin/sh
    set -e
    DIR=internal/store/migrations
    mkdir -p "$DIR"
    TS=$(date +%Y%m%d%H%M%S)
    FILE="$DIR/${TS}_{{ NAME }}.sql"
    cat > "$FILE" <<'EOF'
    -- +goose Up
    -- +goose StatementBegin

    -- +goose StatementEnd

    -- +goose Down
    -- +goose StatementBegin

    -- +goose StatementEnd
    EOF
    echo "created $FILE"

# ============================================================================
# Version Management
# ============================================================================

# Show current version (latest git tag)
version:
    @git describe --tags --abbrev=0 2>/dev/null || echo "no tags yet"

# Bump patch version (vX.Y.Z -> vX.Y.Z+1)
bump-patch:
    @just _bump patch

# Bump minor version (vX.Y.Z -> vX.Y+1.0)
bump-minor:
    @just _bump minor

# Bump major version (vX.Y.Z -> vX+1.0.0)
bump-major:
    @just _bump major

_bump KIND:
    #!/bin/sh
    set -e
    CURRENT=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
    echo "Current version: $CURRENT"
    V=${CURRENT#v}
    MAJOR=$(echo "$V" | cut -d. -f1)
    MINOR=$(echo "$V" | cut -d. -f2)
    PATCH=$(echo "$V" | cut -d. -f3)
    case "{{ KIND }}" in
      patch) PATCH=$((PATCH+1)) ;;
      minor) MINOR=$((MINOR+1)); PATCH=0 ;;
      major) MAJOR=$((MAJOR+1)); MINOR=0; PATCH=0 ;;
      *) echo "unknown bump kind: {{ KIND }}"; exit 1 ;;
    esac
    NEW="v$MAJOR.$MINOR.$PATCH"
    echo "New version:     $NEW"
    git tag -a "$NEW" -m "release $NEW"
    echo ""
    echo "Created tag $NEW"
    echo "Push with:  git push origin main --tags"

# Bump patch, push tag (triggers release workflow once configured)
release-patch: bump-patch
    git push origin main --tags

# Bump minor, push tag
release-minor: bump-minor
    git push origin main --tags

# Bump major, push tag
release-major: bump-major
    git push origin main --tags

# ============================================================================
# Pre-commit
# ============================================================================

# Run before committing
pre-commit: lint-fix test
    @echo "Ready to commit"
