package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

func newTestAdapter(runner Runner) *Adapter {
	cfg := &config.Config{}
	cfg.AI.ClaudeCode.Bin = "claude"
	a := New(cfg)
	a.Runner = runner
	return a
}

func TestAsk_ParsesResultField(t *testing.T) {
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"result": "hello there", "session_id": "x"}`), nil
	}
	a := newTestAdapter(runner)
	res, err := a.Ask(context.Background(), "ping")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if res.Text != "hello there" {
		t.Errorf("Text = %q", res.Text)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", res.Duration)
	}
}

func TestAsk_EmptyResultIsError(t *testing.T) {
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"unrelated": "thing"}`), nil
	}
	a := newTestAdapter(runner)
	if _, err := a.Ask(context.Background(), "ping"); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("want ErrEmptyResponse, got %v", err)
	}
}

func TestSuggest_ParsesMarkers(t *testing.T) {
	body := `---SUMMARY---
Tighten quoting.
---DIFF---
--- a/~/.zshrc
+++ b/~/.zshrc
@@ -1 +1 @@
-foo=bar
+foo="bar"
---END---
`
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return mustJSON(map[string]any{"result": body}), nil
	}
	a := newTestAdapter(runner)
	res, err := a.Suggest(context.Background(), SuggestInput{
		File: tracker.File{DisplayPath: "~/.zshrc"},
	})
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if res.Summary != "Tighten quoting." {
		t.Errorf("Summary = %q", res.Summary)
	}
	if !strings.HasPrefix(res.Diff, "--- a/") {
		t.Errorf("Diff = %q", res.Diff)
	}
}

func TestSuggest_MalformedDiff(t *testing.T) {
	body := `---SUMMARY---
nope
---DIFF---
this is not a diff at all
---END---`
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return mustJSON(map[string]any{"result": body}), nil
	}
	a := newTestAdapter(runner)
	_, err := a.Suggest(context.Background(), SuggestInput{
		File: tracker.File{DisplayPath: "~/.zshrc"},
	})
	if !errors.Is(err, ErrMalformedDiff) {
		t.Fatalf("want ErrMalformedDiff, got %v", err)
	}
}

func TestBuildArgs(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.ClaudeCode.Bin = "mycli"
	cfg.AI.ClaudeCode.Model = "mymodel"
	cfg.AI.ClaudeCode.ExtraArgs = []string{"--foo", "bar"}
	a := New(cfg)
	got := a.BuildArgs("hello")
	want := []string{"-p", "hello", "--output-format=json", "--model", "mymodel", "--foo", "bar"}
	if len(got) != len(want) {
		t.Fatalf("len = %d want %d (got %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q want %q", i, got[i], want[i])
		}
	}
}

func mustJSON(v map[string]any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
