package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// newSourceDB stands up a second migrated state DB inside the
// test's tempdir so cmd/dfm tests can drive `dfm state import`
// with a real --from URL.
func newSourceDB(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "src.db")
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + path}}
	s, err := store.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("store.New src: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return "file://" + path, s
}

func TestStateImportCmd_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	srcURL, src := newSourceDB(t)
	if _, err := src.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (id, path, display_path, added_at)
		 VALUES (1, '/srcpath', '/srcpath', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed src: %v", err)
	}

	cmd := newStateCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"import", "--from", srcURL, "--tables", "tracked_files", "--yes", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v -- output: %s", err, out.String())
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v -- %s", err, out.String())
	}
	if got["imported"].(float64) != 1 {
		t.Fatalf("imported = %v, want 1", got["imported"])
	}

	var n int
	if err := env.Store.DB().QueryRow(`SELECT COUNT(*) FROM tracked_files`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("target rows = %d, want 1", n)
	}
}

func TestStateImportCmd_RefusesSameURL(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	cmd := newStateCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"import", "--from", env.Cfg.State.URL, "--yes"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error, got nil; output: %s", out.String())
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(%d), got %v", exitResolveErr, err)
	}
}

func TestStateImportCmd_DryRunNoWrites(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	srcURL, src := newSourceDB(t)
	if _, err := src.DB().ExecContext(ctx,
		`INSERT INTO tracked_files (id, path, display_path, added_at)
		 VALUES (1, '/p', '/p', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := newStateCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"import", "--from", srcURL, "--dry-run", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got["dry_run"].(bool) != true || got["imported"].(float64) != 1 {
		t.Fatalf("unexpected result: %v", got)
	}

	var n int
	if err := env.Store.DB().QueryRow(`SELECT COUNT(*) FROM tracked_files`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("dry-run wrote %d rows", n)
	}
}

func TestStateImportCmd_MissingFromFlag(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	cmd := newStateCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"import", "--yes"})

	err := cmd.Execute()
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(%d), got %v", exitResolveErr, err)
	}
}

func TestSameStateURL(t *testing.T) {
	if !sameStateURL("file:///tmp/a", "file:///tmp/a") {
		t.Fatal("identical file URLs should match")
	}
	if sameStateURL("file:///tmp/a", "file:///tmp/b") {
		t.Fatal("different paths should not match")
	}
	if !sameStateURL("libsql://example.com?authToken=AAA", "libsql://example.com?authToken=BBB") {
		t.Fatal("same host+path with differing tokens should match")
	}
	if sameStateURL("", "file:///x") {
		t.Fatal("empty should not match")
	}
}

func TestStateSourceDisplay_RedactsURLDetails(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"remote", "libsql://user:userinfo-secret@example.test/private-path?authToken=query-secret#fragment-secret", "libsql://example.test"},
		{"padded", "  https://example.test/private-path?token=query-secret  ", "https://example.test"},
		{"malformed escape", "libsql://example.test/%zz?authToken=query-secret", "<redacted URL>"},
		{"malformed authority", "libsql://user:userinfo-secret@?authToken=query-secret", "<redacted URL>"},
		{"opaque", "libsql:userinfo-secret?authToken=query-secret", "<redacted URL>"},
		{"local URL", "file:///tmp/source.db", "file:///tmp/source.db"},
		{"local path", "/tmp/source.db", "/tmp/source.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stateSourceDisplay(tc.raw); got != tc.want {
				t.Fatalf("display = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStateImportCmd_SameSourceErrorRedactsURLDetails(t *testing.T) {
	for _, raw := range []string{
		"libsql://user:userinfo-secret@example.test/private-path?authToken=query-secret#fragment-secret",
		"libsql://example.test/%zz?authToken=query-secret",
		"libsql:userinfo-secret?authToken=query-secret",
	} {
		cfg := &config.Config{State: config.StateConfig{URL: raw}}
		cmd := newStateCmd()
		cmd.SetContext(config.WithContext(context.Background(), cfg))
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"import", "--from", raw, "--yes"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected same-source refusal")
		}
		message := err.Error() + out.String()
		for _, secret := range []string{"userinfo-secret", "private-path", "query-secret", "fragment-secret"} {
			if strings.Contains(message, secret) {
				t.Fatal("same-source error exposed URL details")
			}
		}
	}
}

func TestStateSourceOpenError_HidesDriverDetailsAndPreservesCause(t *testing.T) {
	cause := errors.New("driver rejected libsql://user:userinfo-secret@example.test/private-path?authToken=query-secret")
	err := &stateSourceOpenError{source: "libsql://example.test", cause: cause}
	for _, secret := range []string{"userinfo-secret", "private-path", "query-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("source-open error exposed driver URL details")
		}
	}
	if !errors.Is(err, cause) {
		t.Fatal("source-open error lost its original cause")
	}
}

func TestOpenSourceDB_MalformedRemoteURLReturnsSafeError(t *testing.T) {
	// Parsing fails before any token is extracted, so the store refuses the
	// remote source without opening a cache or contacting a remote server.
	_, _, err := openSourceDB(context.Background(), "libsql://example.test/%zz?authToken=query-secret")
	if err == nil {
		t.Fatal("expected source-open error")
	}
	if strings.Contains(err.Error(), "query-secret") || strings.Contains(err.Error(), "%zz") {
		t.Fatal("source-open error exposed malformed URL details")
	}
}
