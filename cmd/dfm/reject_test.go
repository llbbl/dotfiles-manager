package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/apply"
	"github.com/llbbl/dotfiles-manager/internal/config"
)

func TestRejectCmd_AcceptsIDCopiedFromSuggestionsTable(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)
	_, fullID := trackFixtureWithSuggestion(t, ctx, env.Store)

	listOut, _, err := runCmd(t, newSuggestionsCmd(), ctx)
	if err != nil {
		t.Fatalf("suggestions: %v", err)
	}
	shortID := firstTableID(t, listOut)
	if len(shortID) != 10 || !strings.HasPrefix(fullID, shortID) {
		t.Fatalf("short id %q is not a 10-char prefix of %q", shortID, fullID)
	}

	out, _, err := runCmd(t, newRejectCmd(), ctx, "--json", shortID)
	if err != nil {
		t.Fatalf("reject with truncated id: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("json: %v -- %s", err, out)
	}
	if res["id"] != fullID {
		t.Errorf("json id = %v, want the resolved full id %s", res["id"], fullID)
	}

	sg, err := apply.NewRepo(env.Store).Get(ctx, fullID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if sg.Status != apply.StatusRejected {
		t.Errorf("status = %s, want %s", sg.Status, apply.StatusRejected)
	}
}

func TestRejectCmd_AmbiguousPrefixListsCandidates(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	const (
		idA = "01HQAAAAAAAAAAAAAAAAAAAAAA"
		idB = "01HQAAAAAABBBBBBBBBBBBBBBB"
	)
	insertSuggestionWithID(t, env.Ctx, env.Store, idA)
	insertSuggestionWithID(t, env.Ctx, env.Store, idB)

	_, stderr, err := runCmd(t, newRejectCmd(), ctx, idA[:10])
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitAmbiguousID {
		t.Errorf("exit = %d, want %d", ee.code, exitAmbiguousID)
	}
	for _, id := range []string{idA, idB} {
		if !strings.Contains(stderr, id) {
			t.Errorf("stderr missing candidate %s:\n%s", id, stderr)
		}
	}
}

func TestRejectCmd_UnknownPrefixSaysPrefixMatchedNothing(t *testing.T) {
	env := newTestEnv(t)
	ctx := config.WithContext(env.Ctx, env.Cfg)

	_, _, err := runCmd(t, newRejectCmd(), ctx, "NOPE")
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exitError, got %v", err)
	}
	if ee.code != exitResolveErr {
		t.Errorf("exit = %d, want %d", ee.code, exitResolveErr)
	}
	if !strings.Contains(ee.msg, "no suggestion matches id prefix") {
		t.Errorf("msg = %q", ee.msg)
	}
}
