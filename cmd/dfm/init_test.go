package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

// TestInit_HappyPathFreshConfig drives the cobra `dfm init` command end
// to end in non-interactive mode and verifies the wizard wrote a usable
// config.toml at the requested path with the documented defaults.
func TestInit_HappyPathFreshConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// The cobra command reads flagConfigPath (a package-level var set
	// by the persistent --config flag). Set it directly since we're
	// bypassing the root command's flag parsing.
	prev := flagConfigPath
	flagConfigPath = cfgPath
	t.Cleanup(func() { flagConfigPath = prev })

	cmd := newInitCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--yes"})

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init --yes: %v\noutput: %s", err, out.String())
	}

	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.AI.Provider != "claude-code" {
		t.Errorf("ai.provider = %q, want claude-code", got.AI.Provider)
	}
	if got.AI.ClaudeCode.Model != "sonnet" {
		t.Errorf("ai.claude-code.model = %q, want sonnet", got.AI.ClaudeCode.Model)
	}
}
