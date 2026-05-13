package dlog

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNew_LevelOff_ReturnsDiscard(t *testing.T) {
	withEnv(t, map[string]string{EnvLevel: "off"})
	l, c, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	if l != Discard {
		t.Fatalf("expected Discard logger when level=off")
	}
	if l.Enabled(context.Background(), slog.LevelError) {
		t.Fatalf("Discard should not be enabled at Error level")
	}
}

func TestNew_LevelUnset_DefaultsOff(t *testing.T) {
	withEnv(t, map[string]string{EnvLevel: ""})
	l, c, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	if l != Discard {
		t.Fatalf("expected Discard logger when level unset")
	}
}

func TestNew_ExplicitLevel_OverridesEnv(t *testing.T) {
	// Env says off, explicit arg says debug — explicit must win.
	withEnv(t, map[string]string{EnvLevel: "off"})
	l, c, err := New("debug")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	if !l.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatalf("explicit debug level should enable debug")
	}
}

func TestNew_EmptyArg_FallsBackToEnv(t *testing.T) {
	withEnv(t, map[string]string{EnvLevel: "debug"})
	l, c, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	if !l.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatalf("empty arg should fall back to DFM_LOG_LEVEL=debug")
	}
}

func TestNew_ErrorLevel_FiltersDebug(t *testing.T) {
	withEnv(t, nil)
	l, c, err := New("error")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	if l.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatalf("error level should not enable debug")
	}
	if !l.Enabled(context.Background(), slog.LevelError) {
		t.Fatalf("error level should enable error")
	}
}

func TestNew_DebugJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dlog.jsonl")
	withEnv(t, map[string]string{
		EnvFormat: "json",
		EnvDest:   "file:" + path,
	})
	l, c, err := New("debug")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.Debug("hello", "k", "v")
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), string(data))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("json: %v -- %q", err, lines[0])
	}
	if rec["level"] != "DEBUG" {
		t.Fatalf("expected level=DEBUG, got %v", rec["level"])
	}
	if rec["msg"] != "hello" {
		t.Fatalf("expected msg=hello, got %v", rec["msg"])
	}
	if rec["k"] != "v" {
		t.Fatalf("expected k=v, got %v", rec["k"])
	}
}

func TestNew_WarnLevel_FiltersDebug(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dlog.txt")
	withEnv(t, map[string]string{
		EnvFormat: "text",
		EnvDest:   "file:" + path,
	})
	l, c, err := New("warn")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.Debug("nope")
	l.Warn("yep")
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if strings.Contains(s, "nope") {
		t.Fatalf("debug should be filtered at warn level: %q", s)
	}
	if !strings.Contains(s, "yep") {
		t.Fatalf("warn record missing: %q", s)
	}
}

func TestNew_UnknownLevel_FallsBackToOff(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldErr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = oldErr }()

	withEnv(t, nil)
	l, c, err := New("garbage")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = w.Close()

	br := bufio.NewReader(r)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, EnvLevel) {
		t.Fatalf("expected fallback warning mentioning %s, got %q", EnvLevel, line)
	}
	if l != Discard {
		t.Fatalf("expected Discard logger after fallback")
	}
}

func TestNew_ConcurrentCalls_NoRace(t *testing.T) {
	// Regression: the previous NewWithLevel mutated os.Setenv around the
	// call, which made concurrent invocations latently racy. The new
	// API takes level as an argument; this test pins that contract.
	withEnv(t, nil)
	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			l, c, err := New("debug")
			if err != nil {
				t.Errorf("New: %v", err)
				return
			}
			defer func() { _ = c.Close() }()
			if !l.Enabled(context.Background(), slog.LevelDebug) {
				t.Errorf("expected debug level enabled")
			}
		}()
	}
	wg.Wait()
}

func TestIntoFromRoundTrip(t *testing.T) {
	l := slog.Default()
	ctx := Into(context.Background(), l)
	if got := From(ctx); got != l {
		t.Fatalf("From did not return the logger we put in")
	}
}

func TestFrom_NoLogger_ReturnsDiscard(t *testing.T) {
	got := From(context.Background())
	if got != Discard {
		t.Fatalf("expected Discard from empty context")
	}
	// Must not panic.
	got.Debug("x")
}

func TestFrom_NilContext_ReturnsDiscard(t *testing.T) {
	got := From(context.TODO())
	if got != Discard {
		t.Fatalf("expected Discard from nil context")
	}
}

func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{EnvLevel, EnvDest, EnvFormat} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}
