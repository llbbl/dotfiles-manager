// Package ai defines the provider interface used by the ask and suggest
// commands, along with the small constructor that selects a concrete
// implementation based on configuration.
package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/ai/claudecode"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
)

// AskRequest is a free-form question.
type AskRequest struct {
	Prompt string
}

// AskResponse carries the assistant's answer plus a wall-clock duration
// measured by the adapter.
type AskResponse struct {
	Text     string
	Duration time.Duration
}

// SuggestRequest carries everything needed to ask for a patch against
// one tracked file.
type SuggestRequest struct {
	File    tracker.File
	Content []byte
	Goal    string
}

// SuggestResponse is the structured result of a Suggest call.
type SuggestResponse struct {
	Diff     string
	Summary  string
	Duration time.Duration
}

// Provider is the small interface every AI adapter implements.
type Provider interface {
	Name() string
	Ask(ctx context.Context, req AskRequest) (AskResponse, error)
	Suggest(ctx context.Context, req SuggestRequest) (SuggestResponse, error)
}

// Error sentinels surfaced to the CLI and to tests.
var (
	ErrEmptyResponse       = errors.New("ai: empty response")
	ErrMalformedDiff       = errors.New("ai: malformed diff")
	ErrProviderUnsupported = errors.New("ai: unsupported provider")
)

// New returns the provider configured in cfg.AI.Provider.
func New(cfg *config.Config) (Provider, error) {
	if cfg == nil {
		return nil, errors.New("ai: nil config")
	}
	switch cfg.AI.Provider {
	case "claude-code":
		return &claudeProvider{adapter: claudecode.New(cfg)}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrProviderUnsupported, cfg.AI.Provider)
	}
}

// claudeProvider adapts *claudecode.Adapter to the Provider interface
// and translates between this package's types and the adapter's.
type claudeProvider struct {
	adapter *claudecode.Adapter
}

// Adapter exposes the underlying Claude Code adapter for tests that
// need to override the runner.
func (p *claudeProvider) Adapter() *claudecode.Adapter { return p.adapter }

func (p *claudeProvider) Name() string { return p.adapter.Name() }

func (p *claudeProvider) Ask(ctx context.Context, req AskRequest) (AskResponse, error) {
	res, err := p.adapter.Ask(ctx, req.Prompt)
	if err != nil {
		return AskResponse{}, translateErr(err)
	}
	return AskResponse{Text: res.Text, Duration: res.Duration}, nil
}

func (p *claudeProvider) Suggest(ctx context.Context, req SuggestRequest) (SuggestResponse, error) {
	res, err := p.adapter.Suggest(ctx, claudecode.SuggestInput{
		File:    req.File,
		Content: req.Content,
		Goal:    req.Goal,
	})
	if err != nil {
		return SuggestResponse{}, translateErr(err)
	}
	return SuggestResponse{
		Diff:     res.Diff,
		Summary:  res.Summary,
		Duration: res.Duration,
	}, nil
}

// translateErr maps subpackage sentinels to this package's sentinels so
// callers only need to know one set.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, claudecode.ErrEmptyResponse) {
		return fmt.Errorf("%w: %s", ErrEmptyResponse, err.Error())
	}
	if errors.Is(err, claudecode.ErrMalformedDiff) {
		return fmt.Errorf("%w: %s", ErrMalformedDiff, err.Error())
	}
	return err
}
