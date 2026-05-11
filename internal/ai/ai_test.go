package ai

import (
	"errors"
	"strings"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func TestNew_ClaudeCode(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.Provider = "claude-code"
	cfg.AI.ClaudeCode.Bin = "claude"
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p == nil {
		t.Fatal("provider is nil")
	}
	if p.Name() != "claude-code" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestNew_Unsupported(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.Provider = "made-up"
	_, err := New(cfg)
	if !errors.Is(err, ErrProviderUnsupported) {
		t.Fatalf("want ErrProviderUnsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "made-up") {
		t.Errorf("error should mention the offending name: %v", err)
	}
}
