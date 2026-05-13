package main

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/store"
)

// configWithCtx is a tiny alias so tests don't have to import
// the config package solely for one call site per file.
func configWithCtx(ctx context.Context, cfg *config.Config) context.Context {
	return config.WithContext(ctx, cfg)
}

func TestMigrateCmd_Status_PrintsSummary(t *testing.T) {
	ctx, _, cfg, _ := setupApplyCmdEnv(t)
	ctx = configWithCtx(ctx, cfg)
	root := newMigrateCmd()
	root.SetContext(ctx)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "dfm migrate: current version") {
		t.Errorf("missing summary line in:\n%s", got)
	}
	if strings.Contains(got, "goose: no migrations") {
		t.Errorf("goose stdlib output leaked into stdout:\n%s", got)
	}
}

// TestListCmd_NoGooseStdlibLeak verifies that running a normal
// store-opening command (`dfm list`) emits zero `goose:` substrings
// through Go's stdlib `log` package. Goose v3's default logger
// targets log.Default(); if our adapter weren't installed via
// configureGoose, idempotent migration runs would print
// "goose: no migrations to run. current version: N" to stderr.
func TestListCmd_NoGooseStdlibLeak(t *testing.T) {
	ctx, _, cfg, _ := setupApplyCmdEnv(t)
	ctx = configWithCtx(ctx, cfg)

	// Capture anything written via stdlib log.
	var stdlibBuf bytes.Buffer
	prev := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&stdlibBuf)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(prevFlags)
	})

	// Open a store the same way commands do — this is what
	// historically triggered goose's stdlib log on the second
	// invocation in a single process.
	s1, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New (1): %v", err)
	}
	defer s1.Close()
	s2, err := store.New(ctx, cfg)
	if err != nil {
		t.Fatalf("store.New (2): %v", err)
	}
	defer s2.Close()

	cmd := newListCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}

	if strings.Contains(stdlibBuf.String(), "goose:") {
		t.Errorf("stdlib log leaked goose lines:\n%s", stdlibBuf.String())
	}
	if strings.Contains(out.String(), "goose:") {
		t.Errorf("command output leaked goose lines:\n%s", out.String())
	}
}

func TestMigrateCmd_Up_AlreadyAtCurrentVersion(t *testing.T) {
	ctx, _, cfg, _ := setupApplyCmdEnv(t)
	ctx = configWithCtx(ctx, cfg)
	root := newMigrateCmd()
	root.SetContext(ctx)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"up"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\nout: %s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "already at version") {
		t.Errorf("expected 'already at version', got:\n%s", got)
	}
}
