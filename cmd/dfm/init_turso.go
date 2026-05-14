package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/fsx"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// runTurso shells out to the `turso` CLI with a scrubbed env. The env
// allowlist mirrors scrubGitEnv plus TURSO_* so an existing
// TURSO_AUTH_TOKEN is honored by the CLI.
func runTurso(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "turso", args...)
	cmd.Env = scrubTursoEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func scrubTursoEnv() []string {
	keep := []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR"}
	var out []string
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TURSO_") {
			out = append(out, e)
		}
	}
	return out
}

// libsqlURLHost returns just the host portion of a libsql:// URL, with
// no path/query. Used for audit logging — never log the full URL.
func libsqlURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// writeTursoEnvFile writes TURSO_AUTH_TOKEN=<token> to path with mode
// 0600. If the file already exists, any existing TURSO_AUTH_TOKEN= line
// is replaced in place; other lines are preserved. If no such line is
// present, one is appended.
func writeTursoEnvFile(path, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	var existing []string
	if data, err := os.ReadFile(path); err == nil {
		// Preserve original line content (sans trailing newline) so we
		// don't munge whitespace the user may have added.
		existing = append(existing, strings.Split(string(data), "\n")...)
		// strings.Split adds a trailing empty element when the file
		// ends in "\n"; drop it so we don't accumulate blank lines.
		if n := len(existing); n > 0 && existing[n-1] == "" {
			existing = existing[:n-1]
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	newLine := "TURSO_AUTH_TOKEN=" + token
	replaced := false
	for i, line := range existing {
		if strings.HasPrefix(strings.TrimSpace(line), "TURSO_AUTH_TOKEN=") {
			existing[i] = newLine
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, newLine)
	}

	out := strings.Join(existing, "\n") + "\n"
	// The canonical helper applies mode to the temp file before rename,
	// so the on-disk file is 0600 from first appearance.
	return fsx.AtomicWrite(path, []byte(out), 0o600)
}

// extractTursoToken takes the stdout of `turso db tokens create` and
// returns the token. Most CLI versions emit just the token; some emit
// a header line first. We take the last non-empty line.
func extractTursoToken(stdout string) string {
	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

// tursoEnvFilePath returns ~/.local/share/dotfiles/.env (or the
// XDG_DATA_HOME equivalent), matching where state.db and backups live.
func tursoEnvFilePath() (string, error) {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "dotfiles", ".env"), nil
}

// runTursoInit performs the full Turso setup flow as described in
// SPEC.md §11. It is orthogonal to the repo init flow.
//
// TODO(future): support --auto-install to run the upstream installer
// (brew or `curl ... | bash`) on the user's behalf. For now, we print
// the command and exit so the user can run it themselves.
func runTursoInit(ctx context.Context, cfg *config.Config, cfgPath, dbName string) error {
	// 1. Detect turso CLI.
	if _, err := exec.LookPath("turso"); err != nil {
		if runtime.GOOS == "darwin" {
			if _, herr := exec.LookPath("brew"); herr == nil {
				fmt.Fprintln(os.Stderr,
					"turso CLI not found. Install with: brew install tursodatabase/tap/turso")
				os.Exit(exitResolveErr)
			}
		}
		fmt.Fprintln(os.Stderr,
			"turso CLI not found. Install: https://docs.turso.tech/cli/installation")
		os.Exit(exitResolveErr)
	}

	// 2. Check auth.
	if _, errOut, err := runTurso(ctx, "auth", "whoami"); err != nil {
		_ = errOut
		fmt.Fprintln(os.Stderr,
			"Not authenticated. Run: turso auth login (opens browser), then re-run with --turso")
		os.Exit(exitResolveErr)
	}

	// 3. Check if DB exists (use `db show` — exit 0 means present).
	dbExists := false
	if _, _, err := runTurso(ctx, "db", "show", dbName); err == nil {
		dbExists = true
		fmt.Printf("Database %q already exists. Reusing.\n", dbName)
	}

	// 4. Create the DB.
	if !dbExists {
		if _, errOut, err := runTurso(ctx, "db", "create", dbName); err != nil {
			return fmt.Errorf("turso db create %s: %w: %s", dbName, err, strings.TrimSpace(errOut))
		}
		fmt.Printf("Created Turso DB %q.\n", dbName)
	}

	// 5. Get URL.
	urlOut, urlErr, err := runTurso(ctx, "db", "show", dbName, "--url")
	if err != nil {
		return fmt.Errorf("turso db show --url: %w: %s", err, strings.TrimSpace(urlErr))
	}
	dbURL := strings.TrimSpace(urlOut)
	if !strings.HasPrefix(dbURL, "libsql://") {
		return fmt.Errorf("unexpected URL from turso db show --url: not a libsql:// URL")
	}

	// 6. Get token.
	tokOut, tokErr, err := runTurso(ctx, "db", "tokens", "create", dbName)
	if err != nil {
		return fmt.Errorf("turso db tokens create %s: %w: %s", dbName, err, strings.TrimSpace(tokErr))
	}
	token := extractTursoToken(tokOut)
	if token == "" {
		return fmt.Errorf("empty token from turso db tokens create")
	}

	// 7. Write URL to config.
	cfg.State.URL = dbURL
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("save config %s: %w", cfgPath, err)
	}

	// 8. Write token to ~/.local/share/dotfiles/.env.
	envPath, err := tursoEnvFilePath()
	if err != nil {
		return fmt.Errorf("resolve env path: %w", err)
	}
	if err := writeTursoEnvFile(envPath, token); err != nil {
		return err
	}

	// 9. Run migrations against the newly-configured remote. We construct
	// a fresh config struct with the URL+token in memory rather than
	// mutating the caller's cfg — the token isn't saved to config.toml,
	// it lives only in the .env file.
	migrateCfg := config.Config{}
	migrateCfg.State.URL = dbURL
	migrateCfg.State.AuthToken = token
	s, mErr := store.New(ctx, &migrateCfg)
	if mErr != nil {
		fmt.Fprintf(os.Stderr, `
✗ Turso remote configured but migrations failed: %v
  Run: dfm migrate up
`, mErr)
		audit.Log(ctx, "init.turso", map[string]any{
			"db_name":  dbName,
			"url_host": libsqlURLHost(dbURL),
		})
		os.Exit(1)
	}
	defer func() { _ = s.Close() }()

	// 10. Success block.
	fmt.Printf(`
✓ Turso remote configured.
  DB:         %s
  URL:        libsql://....  (written to %s)
  Token:      %s (0600)
  Migrations: applied

Next step: ensure your shell loads %s, or export TURSO_AUTH_TOKEN
in your environment. dfm reads $TURSO_AUTH_TOKEN at runtime.
`, dbName, cfgPath, envPath, envPath)

	// 11. Audit event — host only, never the full URL or token.
	audit.Log(ctx, "init.turso", map[string]any{
		"db_name":    dbName,
		"url_host":   libsqlURLHost(dbURL),
		"migrations": "applied",
	})
	return nil
}

