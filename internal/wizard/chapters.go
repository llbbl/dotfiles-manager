package wizard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

// chapterConfigPath resolves where the config will live. The path
// comes from (in order): --config (Options.ConfigPath), then
// config.DefaultPath(). In --yes mode we never prompt; otherwise we
// confirm the path and let the user override.
func chapterConfigPath(in *bufio.Reader, out io.Writer, opts Options) (string, error) {
	def := opts.ConfigPath
	if def == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return "", err
		}
		def = p
	}
	if opts.Yes {
		return def, nil
	}
	ans, err := AskLine(in, out, PromptOpts{
		Question: "Config path",
		Default:  def,
	})
	if err != nil {
		return "", err
	}
	return ans, nil
}

// chapterAI prompts for the claude-code adapter settings. Today we
// only support one provider so we don't ask which one; we just write
// "claude-code" so the file is self-describing.
func chapterAI(in *bufio.Reader, out io.Writer, opts Options, cfg *config.Config, editMode bool) error {
	cfg.AI.Provider = "claude-code"

	// Binary path.
	binDefault := cfg.AI.ClaudeCode.Bin
	if !editMode || binDefault == "" {
		binDefault = lookupClaude()
	}
	if opts.AIBin != "" {
		cfg.AI.ClaudeCode.Bin = opts.AIBin
	} else if opts.Yes {
		cfg.AI.ClaudeCode.Bin = binDefault
	} else {
		ans, err := AskLine(in, out, PromptOpts{
			Question: "Claude Code binary path",
			Default:  binDefault,
		})
		if err != nil {
			return err
		}
		cfg.AI.ClaudeCode.Bin = ans
	}

	// Model. Recommended default is "sonnet". Empty input is valid
	// (means no --model flag is forwarded by the suggest path).
	modelDefault := "sonnet"
	if editMode && cfg.AI.ClaudeCode.Model != "" {
		modelDefault = cfg.AI.ClaudeCode.Model
	}
	switch {
	case opts.AIModel != "":
		cfg.AI.ClaudeCode.Model = opts.AIModel
	case opts.Yes:
		cfg.AI.ClaudeCode.Model = modelDefault
	default:
		fmt.Fprintln(out, "  AI model: 'sonnet' is the recommended default; 'opus' or a full id like 'claude-sonnet-4-6' also work. Leave empty for the CLI's own default.")
		ans, err := AskLine(in, out, PromptOpts{
			Question: "AI model",
			Default:  modelDefault,
		})
		if err != nil {
			return err
		}
		cfg.AI.ClaudeCode.Model = ans
	}
	return nil
}

// chapterState walks the user through picking a state store. Returns
// (provisionTurso, dbName, err). When provisionTurso is true the cobra
// layer should invoke the existing runTursoInit helper to actually
// create the DB; the wizard only collected the choice.
//
// "Bake from env" path: when the user picks Turso AND
// TURSO_DATABASE_URL/TURSO_AUTH_TOKEN are already in the process env
// (or supplied via --turso-url / --turso-auth-token), the wizard
// writes those values directly into the config and returns
// provisionTurso=false. No CLI shell-out is required.
func chapterState(in *bufio.Reader, out io.Writer, opts Options, cfg *config.Config, editMode bool) (bool, string, error) {
	// Resolve the branch (local vs turso) up front.
	branch := strings.ToLower(opts.State)
	if branch == "" && opts.Turso {
		branch = "turso"
	}

	if branch == "" && !opts.Yes {
		// Detect edit-mode default: if existing config has a libsql://
		// URL, default to turso; otherwise local.
		def := "local"
		if editMode && strings.HasPrefix(cfg.State.URL, "libsql://") {
			def = "turso"
		}
		ans, err := AskLine(in, out, PromptOpts{
			Question: "State store: (a) local SQLite  (b) Turso libSQL",
			Default:  def,
			Validate: func(s string) error {
				switch strings.ToLower(strings.TrimSpace(s)) {
				case "a", "local", "b", "turso":
					return nil
				}
				return fmt.Errorf("answer 'a' (local) or 'b' (turso)")
			},
		})
		if err != nil {
			return false, "", err
		}
		switch strings.ToLower(strings.TrimSpace(ans)) {
		case "b", "turso":
			branch = "turso"
		default:
			branch = "local"
		}
	}
	if branch == "" {
		branch = "local"
	}

	if branch == "local" {
		// Keep Defaults()'s local file URL (or whatever editMode
		// carried over, as long as it's not libsql://).
		if strings.HasPrefix(cfg.State.URL, "libsql://") {
			d := config.Defaults()
			cfg.State.URL = d.State.URL
		}
		cfg.State.AuthToken = ""
		return false, "", nil
	}

	// Turso branch.
	dbName := opts.TursoDBName
	if dbName == "" {
		dbName = "dotfiles-state"
	}

	url := opts.TursoURL
	token := opts.TursoAuthToken
	if url == "" {
		url = os.Getenv("TURSO_DATABASE_URL")
	}
	if token == "" {
		token = os.Getenv("TURSO_AUTH_TOKEN")
	}

	// Non-interactive paths: if either url+token came in via env/flags
	// AND user said --yes (or --turso non-interactively), bake.
	if opts.Yes || opts.Turso || opts.TursoURL != "" || opts.TursoAuthToken != "" {
		if url != "" && token != "" {
			fmt.Fprintln(out, "  Warning: TURSO_AUTH_TOKEN will be written to the config file in plain text.")
			cfg.State.URL = url
			cfg.State.AuthToken = token
			return false, dbName, nil
		}
		// No env/flag values to bake — defer to the CLI provisioning
		// flow (will shell out to `turso`).
		return true, dbName, nil
	}

	// Interactive: offer to bake env values if present.
	if url != "" && token != "" {
		ok, err := AskYesNo(in, out, fmt.Sprintf("Use TURSO_DATABASE_URL/AUTH_TOKEN from your environment (%s)?", url), true)
		if err != nil {
			return false, "", err
		}
		if ok {
			fmt.Fprintln(out, "  Warning: TURSO_AUTH_TOKEN will be written to the config file in plain text.")
			cfg.State.URL = url
			cfg.State.AuthToken = token
			return false, dbName, nil
		}
	}

	// Otherwise prompt for both.
	urlAns, err := AskLine(in, out, PromptOpts{
		Question: "Turso database URL (libsql://...)",
		Default:  url,
		Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("URL is required (or pick local SQLite instead)")
			}
			return nil
		},
	})
	if err != nil {
		return false, "", err
	}
	tokAns, err := AskLine(in, out, PromptOpts{
		Question: "Turso auth token",
		Default:  token,
		Secret:   true,
		Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("auth token is required")
			}
			return nil
		},
	})
	if err != nil {
		return false, "", err
	}
	fmt.Fprintln(out, "  Warning: TURSO_AUTH_TOKEN will be written to the config file in plain text.")
	cfg.State.URL = urlAns
	cfg.State.AuthToken = tokAns
	return false, dbName, nil
}

// chapterRepo collects the backup-repo settings. It does NOT clone or
// create anything — that's the cobra layer's job via runRepoInit. We
// just decide whether to invoke that flow and what URL to point it at.
func chapterRepo(in *bufio.Reader, out io.Writer, opts Options, cfg *config.Config, editMode bool) (bool, bool, error) {
	// Non-interactive shortcuts.
	if opts.RemoteURL != "" {
		cfg.Repo.Remote = opts.RemoteURL
		return true, opts.CreateRemote, nil
	}
	if opts.CreateRemote && cfg.Repo.Remote != "" {
		return true, true, nil
	}
	if opts.Yes {
		// Accept existing value (possibly empty) — skip repo flow.
		return cfg.Repo.Remote != "", false, nil
	}

	def := cfg.Repo.Remote
	if !editMode {
		def = ""
	}
	ans, err := AskLine(in, out, PromptOpts{
		Question: "Backup repo remote URL (leave empty to skip)",
		Default:  def,
	})
	if err != nil {
		return false, false, err
	}
	cfg.Repo.Remote = ans
	if ans == "" {
		return false, false, nil
	}

	createRemote := opts.CreateRemote
	if !createRemote {
		ok, yerr := AskYesNo(in, out, "Create the remote via `gh repo create` if it doesn't exist?", false)
		if yerr != nil {
			return false, false, yerr
		}
		createRemote = ok
	}
	return true, createRemote, nil
}

// chapterTrack offers the optional `dfm track ~/.zshrc` nudge.
// Returns the path the user accepted (or "").
func chapterTrack(in *bufio.Reader, out io.Writer, opts Options) (string, error) {
	if opts.Yes {
		return "", nil
	}
	ok, err := AskYesNo(in, out, "Track ~/.zshrc now?", false)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return "~/.zshrc", nil
}

// chapterSummary prints the closing block and returns the "try this
// next" tip so the cobra layer can re-print it after side effects.
func chapterSummary(out io.Writer, plan *Plan, opts Options) string {
	mode := "wrote"
	if opts.Print {
		mode = "would write"
	}
	fmt.Fprintf(out, "\n%s config: %s (mode 0600)\n", mode, plan.ConfigPath)

	tip := "Try: dfm list"
	if plan.RepoFlow {
		tip = "Try: dfm sync"
	} else if plan.TrackPath != "" {
		tip = "Try: dfm suggest " + plan.TrackPath + " --goal 'tidy this up'"
	}
	fmt.Fprintf(out, "Next: %s\n", tip)
	return tip
}
