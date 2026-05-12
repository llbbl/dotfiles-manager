package dlog

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNew_LevelOff_ReturnsDiscard(t *testing.T) {
	withEnv(t, map[string]string{EnvLevel: "off"})
	l, c, err := New()
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
	l, c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	if l != Discard {
		t.Fatalf("expected Discard logger when level unset")
	}
}

func TestNew_DebugJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dlog.jsonl")
	withEnv(t, map[string]string{
		EnvLevel:  "debug",
		EnvFormat: "json",
		EnvDest:   "file:" + path,
	})
	l, c, err := New()
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
		EnvLevel:  "warn",
		EnvFormat: "text",
		EnvDest:   "file:" + path,
	})
	l, c, err := New()
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

	withEnv(t, map[string]string{EnvLevel: "garbage"})
	l, c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = w.Close()

	br := bufio.NewReader(r)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "DOTFILES_LOG_LEVEL") {
		t.Fatalf("expected fallback warning, got %q", line)
	}
	if l != Discard {
		t.Fatalf("expected Discard logger after fallback")
	}
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
