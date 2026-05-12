package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/llbbl/dotfiles-manager/internal/ai"
	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/spf13/cobra"
)

// providerFactory builds an ai.Provider from the loaded config. Tests
// replace this to inject a fake without touching the claudecode adapter.
var providerFactory = func(cfg *config.Config) (ai.Provider, error) {
	return ai.New(cfg)
}

const (
	exitAIEmpty   = 3
	exitNotFound  = 4
	exitAIGeneric = 5
)

// newAskCmd builds the `dotfiles ask` command, which sends a free-form
// question to the configured AI provider and prints the response. The
// --json flag emits the answer plus provider/duration metadata as JSON;
// every invocation is recorded to the audit log with a hashed prompt.
func newAskCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask a free-form question about your dotfiles",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg := config.FromContext(c.Context())
			if cfg == nil {
				return errors.New("config not loaded")
			}
			prompt := args[0]

			prov, err := providerFactory(cfg)
			if err != nil {
				return exitf(exitResolveErr, "%v", err)
			}

			res, err := prov.Ask(c.Context(), ai.AskRequest{Prompt: prompt})
			fields := map[string]any{
				"provider":      prov.Name(),
				"prompt_sha256": sha256Hex(prompt),
				"duration_ms":   res.Duration.Milliseconds(),
			}
			if err != nil {
				code := classifyExit(err)
				fields["exit_code"] = code
				audit.Log(c.Context(), "ask", fields)
				return exitf(code, "%v", err)
			}

			fields["exit_code"] = 0
			audit.Log(c.Context(), "ask", fields)

			if asJSON {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"text":        res.Text,
					"duration_ms": res.Duration.Milliseconds(),
					"provider":    prov.Name(),
				})
			}
			fmt.Fprintln(c.OutOrStdout(), res.Text)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit response as JSON")
	return cmd
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func classifyExit(err error) int {
	if errors.Is(err, ai.ErrEmptyResponse) {
		return exitAIEmpty
	}
	return exitAIGeneric
}
