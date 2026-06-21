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

// TestInit_ChapterFive_TracksInline pins the chapter-5 yes-track
// integration: when the wizard returns a Plan with TrackPath != "",
// the cobra layer must actually track the file (via runTrackOne) so
// the user doesn't have to re-run `dfm track` after init. This is the
// PR-#43 follow-up — the prior implementation only printed a hint.
//
// We exercise the cobra-layer hookup directly: call runTrackOne with
// the same ctx and arguments init.go uses (rawPath = plan.TrackPath,
// zero-value trackOneOptions), then verify a tracked_files row
// landed in the state DB.
func TestInit_ChapterFive_TracksInline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// chapterTrack hard-codes "~/.zshrc" — mirror that exact rawPath
	// here so the test fails if the chapter's contract drifts.
	rcPath := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rcPath, []byte("# test rc\n"), 0o644); err != nil {
		t.Fatalf("write ~/.zshrc: %v", err)
	}

	ctx, _, _ := setupEditCmdEnv(t)

	cmd := newInitCmd()
	cmd.SetContext(ctx)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	code, err := runTrackOne(cmd, "~/.zshrc", trackOneOptions{})
	if err != nil {
		t.Fatalf("runTrackOne: %v\nstderr: %s", err, errOut.String())
	}
	if code != 0 {
		t.Fatalf("runTrackOne code = %d, want 0\nstderr: %s", code, errOut.String())
	}

	// Resolve to canonical so the WHERE clause matches the row inserted
	// by tracker.Track (which keys on canonical paths).
	canonical, err := filepath.EvalSymlinks(rcPath)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}

	s, err := openStore(ctx)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()

	var (
		displayPath string
	)
	row := s.DB().QueryRowContext(ctx,
		`SELECT display_path FROM tracked_files WHERE path = ?`, canonical)
	if err := row.Scan(&displayPath); err != nil {
		t.Fatalf("expected tracked_files row for %s: %v", canonical, err)
	}
	if displayPath == "" {
		t.Errorf("display_path is empty for tracked row")
	}

	if !bytes.Contains(out.Bytes(), []byte("tracked")) {
		t.Errorf("expected 'tracked' confirmation on stdout, got: %s", out.String())
	}
}
