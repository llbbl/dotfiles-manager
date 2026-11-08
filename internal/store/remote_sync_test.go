package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func newCloseTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + filepath.Join(t.TempDir(), "state.db")}}
	s, _, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRemoteOperationError_SafeCause(t *testing.T) {
	for _, tc := range []struct {
		name, cause, want string
	}{
		{"authentication", "HTTP 401 token-secret row-secret https://private.test/secret", "remote operation failed (HTTP 401)"},
		{"server", "HTTP status code: 500 token-secret row-secret https://private.test/secret", "remote operation failed (HTTP 500)"},
		{"status equals", "status=503 token-secret", "remote operation failed (HTTP 503)"},
		{"too many digits", "HTTP 4010 token-secret", "remote operation failed"},
		{"too few digits", "HTTP 40 token-secret", "remote operation failed"},
		{"invalid status", "HTTP 999 token-secret", "remote operation failed"},
		{"decimal continuation", "HTTP 401.75 bytes read", "remote operation failed"},
		{"comma continuation", "status code: 401,250 rows", "remote operation failed"},
		{"skips non-status field", "status=123-456 row identifier; HTTP 503", "remote operation failed (HTTP 503)"},
		{"driver format", "sync engine operation failed: status=404", "remote operation failed (HTTP 404)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New(tc.cause)
			err := &remoteOperationError{cause: cause}
			if got := err.Error(); got != tc.want {
				t.Errorf("safe error = %q, want %q", got, tc.want)
			}
			if !errors.Is(err, cause) {
				t.Error("safe error did not retain its cause")
			}
		})
	}
	if got := (&remoteOperationError{cause: context.DeadlineExceeded}).Error(); got != "remote operation failed (network unreachable/timeout)" {
		t.Errorf("deadline error = %q", got)
	}
}

func TestStoreClose_PushFailure(t *testing.T) {
	s := newCloseTestStore(t)
	s.target = "libsql://user:password@example.test/private?authToken=url-secret#fragment"
	pushErr := errors.New("driver echoed token-secret, row-secret and " + s.target)
	pushes := 0
	s.push = func(ctx context.Context) error {
		pushes++
		deadline, ok := ctx.Deadline()
		if remaining := time.Until(deadline); !ok || remaining <= 0 || remaining > 30*time.Second {
			t.Error("push context must have a deadline within 30 seconds")
		}
		if err := s.DB().PingContext(ctx); err != nil {
			t.Error("database closed before push")
		}
		return pushErr
	}
	err := s.Close()
	if !errors.Is(err, pushErr) {
		t.Errorf("Close did not wrap the push failure")
	}
	if err == nil {
		t.Error("Close returned nil after a failed push")
	} else {
		for _, want := range []string{"changes saved locally", "libsql://example.test", "they push on the next command that completes successfully"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Close error missing %q", want)
			}
		}
		for _, secret := range []string{"user", "password", "/private", "url-secret", "fragment", "token-secret", "row-secret"} {
			if strings.Contains(err.Error(), secret) {
				t.Error("Close error exposed private driver details")
			}
		}
	}
	if err := s.DB().Ping(); err == nil {
		t.Error("database still open after push failure")
	}
	if !errors.Is(s.Close(), pushErr) || pushes != 1 {
		t.Error("repeated Close did not retain the result without pushing again")
	}
}

func TestStoreClose_SharedRefDoesNotPush(t *testing.T) {
	s := newCloseTestStore(t)
	pushes := 0
	s.push = func(context.Context) error { pushes++; return nil }
	if err := s.SharedRef().Close(); err != nil {
		t.Fatalf("shared Close: %v", err)
	}
	if pushes != 0 {
		t.Error("shared Close pushed changes")
	}
	if err := s.DB().Ping(); err != nil {
		t.Error("shared Close closed the owner's database")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("owning Close: %v", err)
	}
	if pushes != 1 {
		t.Errorf("owner pushed %d times, want 1", pushes)
	}
	if err := s.DB().Ping(); err == nil {
		t.Error("owning Close left the database open")
	}
}

func TestStoreClose_Local(t *testing.T) {
	s := newCloseTestStore(t)
	if s.sync != nil || s.push != nil {
		t.Fatal("local store has remote sync hooks")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.DB().Ping(); err == nil {
		t.Error("local database still open after Close")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
}

func TestRemoteSync_RoundTrip(t *testing.T) {
	remoteURL, token := os.Getenv("DFM_TEST_TURSO_URL"), os.Getenv("DFM_TEST_TURSO_TOKEN")
	if remoteURL == "" || token == "" {
		t.Skip("set DFM_TEST_TURSO_URL and DFM_TEST_TURSO_TOKEN for a disposable Turso database")
	}
	aHome, bHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", bHome)
	cfg := &config.Config{State: config.StateConfig{URL: remoteURL, AuthToken: token}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Seed B's cache before A writes so reopening B must pull the new row.
	seed, err := New(ctx, cfg)
	if err != nil {
		t.Fatal("seed replica B failed")
	}
	if err := seed.Close(); err != nil {
		t.Fatal("close seeded replica B failed")
	}
	t.Setenv("HOME", aHome)
	a, err := New(ctx, cfg)
	if err != nil {
		t.Fatal("open replica A failed")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = a.Close()
		t.Fatal("create test row identifier failed")
	}
	action := fmt.Sprintf("dfm-remote-sync-test-%x", nonce)
	// Cleanup uses its own replica so it can remove the row even after an assertion fails.
	cleanupHome := t.TempDir()
	t.Cleanup(func() {
		t.Setenv("HOME", cleanupHome)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		s, err := New(cleanupCtx, cfg)
		if err != nil {
			t.Error("open cleanup replica failed")
			return
		}
		if _, err := s.DB().ExecContext(cleanupCtx, `DELETE FROM actions WHERE action = ?`, action); err != nil {
			t.Error("delete test action failed")
		}
		if err := s.Close(); err != nil {
			t.Error("push test action cleanup failed")
		}
	})
	if _, err := a.DB().ExecContext(ctx,
		`INSERT INTO actions (ts, action, payload_json) VALUES (?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), action, "{}"); err != nil {
		_ = a.Close()
		t.Fatal("insert test action failed")
	}
	if err := a.Close(); err != nil {
		t.Fatal("close and push replica A failed")
	}
	t.Setenv("HOME", bHome)
	b, err := New(ctx, cfg)
	if err != nil {
		t.Fatal("open replica B failed")
	}
	defer func() {
		if err := b.Close(); err != nil {
			t.Error("close replica B failed")
		}
	}()
	var count int
	if err := b.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE action = ?`, action).Scan(&count); err != nil {
		t.Fatal("read test action from replica B failed")
	}
	if count != 1 {
		t.Errorf("replica B has %d matching actions, want 1", count)
	}
}

// A server that accepts requests but never answers must fail Open within the
// sync bound instead of hanging, whether the stall hits bootstrap or pull.
func TestOpen_StalledServerFailsWithinBound(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	t.Setenv("HOME", t.TempDir())

	old := remoteSyncTimeout
	remoteSyncTimeout = 300 * time.Millisecond
	t.Cleanup(func() { remoteSyncTimeout = old })

	cfg := &config.Config{State: config.StateConfig{URL: srv.URL, AuthToken: "stall-token"}}
	done := make(chan error, 1)
	go func() {
		s, _, err := Open(context.Background(), cfg)
		if err == nil {
			_ = s.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Open against a stalled server succeeded, want an error")
		}
		if strings.Contains(err.Error(), "stall-token") {
			t.Errorf("error leaks the token: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Open hung against a stalled server")
	}
}

// An existing replica whose pull stalls must fail Open within the bound: a pull
// abandoned at its deadline leaves the sync handle unable to connect.
func TestRemoteSync_StalledPullFailsWithinBound(t *testing.T) {
	remoteURL, token := os.Getenv("DFM_TEST_TURSO_URL"), os.Getenv("DFM_TEST_TURSO_TOKEN")
	if remoteURL == "" || token == "" {
		t.Skip("set DFM_TEST_TURSO_URL and DFM_TEST_TURSO_TOKEN for a disposable Turso database")
	}
	upstream, err := url.Parse(strings.Replace(remoteURL, "libsql://", "https://", 1))
	if err != nil {
		t.Fatal("parse test URL")
	}
	var stall atomic.Bool
	release := make(chan struct{})
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Host = upstream.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "test proxy request failed", http.StatusBadGateway)
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stall.Load() {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	t.Setenv("HOME", t.TempDir())

	cfg := &config.Config{State: config.StateConfig{URL: srv.URL, AuthToken: token}}
	s, _, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal("seed replica through proxy failed")
	}
	if err := s.Close(); err != nil {
		t.Fatal("close seeded replica failed")
	}

	old := remoteSyncTimeout
	remoteSyncTimeout = time.Second
	t.Cleanup(func() { remoteSyncTimeout = old })
	stall.Store(true)
	done := make(chan error, 1)
	go func() {
		s, _, err := Open(context.Background(), cfg)
		// A returned store is the failure; it is left unclosed because the wedged
		// handle would block Close.
		_ = s
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Open after a stalled pull returned a store, want an error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Open hung after a stalled pull")
	}
}
