package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/vcs"
	"github.com/llbbl/dotfiles-manager/internal/wizard"
	"github.com/spf13/cobra"
)

const (
	exitInitNoTTY = 4
)

// newInitCmd builds the `dfm init` command, which drives the
// interactive first-run wizard. The wizard scaffolds the config file,
// optionally provisions a Turso libSQL state DB, optionally clones or
// creates the backup repo, and optionally tracks an initial file.
//
// Every chapter has a corresponding flag for non-interactive use; the
// older --remote / --create-remote / --turso flags from the pre-wizard
// implementation continue to work and short-circuit their chapters.
func newInitCmd() *cobra.Command {
	var (
		remoteFlag     string
		createRemote   bool
		yes            bool
		force          bool
		printOnly      bool
		tursoFlag      bool
		tursoDBName    string
		tursoURL       string
		tursoAuthToken string
		stateFlag      string
		aiBin          string
		aiModel        string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "First-run setup wizard: scaffold config, state store, and backup repo",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()

			// Resolve the destination config path: --config wins, else
			// the XDG default. The wizard re-derives this for its own
			// chapter but we need the same value here to decide whether
			// to enter edit mode.
			cfgPath := flagConfigPath
			if cfgPath == "" {
				p, err := config.DefaultPath()
				if err != nil {
					return err
				}
				cfgPath = p
			}

			existing, err := wizard.LoadExisting(cfgPath)
			if err != nil {
				return err
			}

			plan, err := wizard.Run(wizard.Options{
				ConfigPath:     cfgPath,
				Yes:            yes,
				Force:          force,
				Print:          printOnly,
				State:          stateFlag,
				RemoteURL:      remoteFlag,
				CreateRemote:   createRemote,
				Turso:          tursoFlag,
				TursoDBName:    tursoDBName,
				TursoURL:       tursoURL,
				TursoAuthToken: tursoAuthToken,
				AIBin:          aiBin,
				AIModel:        aiModel,
				In:             os.Stdin,
				Out:            c.OutOrStdout(),
			}, existing)
			if err != nil {
				return err
			}

			if printOnly {
				return nil
			}

			// Re-load the freshly-written config so downstream side
			// effects observe what the user just wrote (the cobra
			// context's cfg was populated from the OLD config — likely
			// Defaults() — at PersistentPreRunE time).
			cfg, err := config.Load(plan.ConfigPath)
			if err != nil {
				return err
			}

			if plan.ProvisionTurso {
				if err := runTursoInit(ctx, cfg, plan.ConfigPath, plan.TursoDBName); err != nil {
					return err
				}
				// runTursoInit re-saves config.toml with the URL; reload.
				cfg, err = config.Load(plan.ConfigPath)
				if err != nil {
					return err
				}
			}

			if plan.RepoFlow {
				if cfg.Repo.Local == "" {
					fmt.Fprintln(c.ErrOrStderr(), "init: [repo].local is empty")
					os.Exit(exitResolveErr)
				}
				if err := runRepoInit(c, cfg, plan.CreateRemote, yes); err != nil {
					return err
				}
			}

			if plan.TrackPath != "" {
				fmt.Fprintf(c.OutOrStdout(),
					"  (skipping inline track of %s — run `dfm track %s` to add it)\n",
					plan.TrackPath, plan.TrackPath)
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&remoteFlag, "remote", "", "remote git URL (overrides [repo].remote)")
	cmd.Flags().BoolVar(&createRemote, "create-remote", false, "create the remote via gh if it doesn't exist")
	cmd.Flags().BoolVar(&yes, "yes", false, "non-interactive: accept all defaults")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config without edit-mode pre-fills")
	cmd.Flags().BoolVar(&printOnly, "print", false, "render the would-be config to stdout, write nothing")
	cmd.Flags().BoolVar(&tursoFlag, "turso", false, "set up a Turso libSQL remote DB (state-store chapter)")
	cmd.Flags().StringVar(&tursoDBName, "turso-db-name", "dotfiles-state", "Turso DB name to create or reuse")
	cmd.Flags().StringVar(&tursoURL, "turso-url", "", "libsql:// URL to bake into the config (skips provisioning)")
	cmd.Flags().StringVar(&tursoAuthToken, "turso-auth-token", "", "Turso auth token to bake into the config")
	cmd.Flags().StringVar(&stateFlag, "state", "", "state store: local|turso (selects the state chapter branch)")
	cmd.Flags().StringVar(&aiBin, "ai-bin", "", "path to the Claude Code binary (overrides the AI chapter)")
	cmd.Flags().StringVar(&aiModel, "ai-model", "", "AI model name (overrides the AI chapter)")
	return cmd
}

// runRepoInit performs the repo clone-or-create flow (extracted from
// the original RunE body so --turso can compose orthogonally with it).
func runRepoInit(c *cobra.Command, cfg *config.Config, createRemote, yes bool) error {
	if _, err := vcs.Open(cfg); err == nil {
		fmt.Fprintf(c.OutOrStdout(), "already initialized: %s\n", cfg.Repo.Local)
		return nil
	}

	ctx := c.Context()
	reachable, hasRefs, err := lsRemote(ctx, cfg.Repo.Remote)
	if err != nil && reachable {
		return err
	}
	if reachable && hasRefs {
		if _, err := vcs.Clone(ctx, cfg); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "cloned %s -> %s\n", cfg.Repo.Remote, cfg.Repo.Local)
		audit.Log(ctx, "init", map[string]any{
			"mode":  "clone",
			"local": cfg.Repo.Local,
		})
		return nil
	}

	// Creation flow.
	if !reachable && !createRemote {
		fmt.Fprintf(c.ErrOrStderr(),
			"init: remote %s is unreachable; pass --create-remote to create it via gh\n",
			cfg.Repo.Remote)
		os.Exit(exitInitNoTTY)
	}
	if !createRemote {
		if !isTTY() {
			fmt.Fprintln(c.ErrOrStderr(),
				"init: remote is empty and stdin is not a TTY; rerun with --create-remote")
			os.Exit(exitInitNoTTY)
		}
		if !yes {
			ok, err := confirm(c, "Remote is empty. Create it via `gh repo create` and push? [y/N] ")
			if err != nil {
				return err
			}
			if !ok {
				fmt.Fprintln(c.ErrOrStderr(), "init: aborted")
				os.Exit(exitInitNoTTY)
			}
		}
	}

	if !reachable || !hasRefs {
		if err := ghCreatePrivate(ctx, cfg.Repo.Remote); err != nil {
			return err
		}
	}

	r, err := vcs.InitLocal(ctx, cfg)
	if err != nil {
		return err
	}
	name := repoNameFromRemote(cfg.Repo.Remote)
	readme := fmt.Sprintf("# %s\n\nPrivate dotfiles-manager backup.\n", name)
	if err := r.WriteFile("README.md", []byte(readme)); err != nil {
		return err
	}
	if _, err := r.CommitAll(ctx, "init: dotfiles-manager backup repo"); err != nil {
		return err
	}
	if err := r.Push(ctx); err != nil {
		return err
	}

	audit.Log(ctx, "init", map[string]any{
		"mode":  "create",
		"local": cfg.Repo.Local,
	})
	fmt.Fprintf(c.OutOrStdout(), "initialized %s -> %s\n", cfg.Repo.Remote, cfg.Repo.Local)
	return nil
}

// lsRemote returns (reachable, hasRefs, err). A reachable empty repo is
// (true, false, nil). Network failures are (false, false, err-or-nil).
func lsRemote(ctx context.Context, remote string) (bool, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", remote)
	cmd.Env = scrubGitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// Could be auth failure, network, or empty repo. We treat
		// exit code 128 with "Repository not found" or "could not read
		// from remote" as unreachable; an empty-but-existing repo
		// returns exit code 0 with empty stdout (so we never get here).
		errLower := strings.ToLower(stderr.String())
		if strings.Contains(errLower, "repository not found") ||
			strings.Contains(errLower, "could not read") ||
			strings.Contains(errLower, "does not exist") ||
			strings.Contains(errLower, "not found") {
			return false, false, nil
		}
		return false, false, fmt.Errorf("git ls-remote: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if strings.TrimSpace(stdout.String()) == "" {
		return true, false, nil
	}
	return true, true, nil
}

func scrubGitEnv() []string {
	keep := []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "SSH_AUTH_SOCK", "TMPDIR"}
	var out []string
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GIT_") {
			out = append(out, e)
		}
	}
	return out
}

var sshURLRegexp = regexp.MustCompile(`^(?:git@|ssh://git@)([^:/]+)[:/](.+?)(?:\.git)?$`)
var httpsURLRegexp = regexp.MustCompile(`^https?://[^/]+/(.+?)(?:\.git)?$`)

// parseOwnerName extracts owner/name from common GitHub remote URLs.
func parseOwnerName(remote string) (string, string, bool) {
	if m := sshURLRegexp.FindStringSubmatch(remote); m != nil {
		parts := strings.SplitN(m[2], "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1], true
		}
	}
	if m := httpsURLRegexp.FindStringSubmatch(remote); m != nil {
		parts := strings.SplitN(m[1], "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

func repoNameFromRemote(remote string) string {
	if _, n, ok := parseOwnerName(remote); ok {
		return n
	}
	base := filepath.Base(remote)
	return strings.TrimSuffix(base, ".git")
}

func ghCreatePrivate(ctx context.Context, remote string) error {
	owner, name, ok := parseOwnerName(remote)
	if !ok {
		return fmt.Errorf("init: cannot parse owner/name from remote %q", remote)
	}
	slug := owner + "/" + name
	cmd := exec.CommandContext(ctx, "gh", "repo", "create", slug,
		"--private", "--description", "dotfiles-manager backup repo")
	cmd.Env = scrubGitEnv()
	// Allow gh's own auth env vars through.
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GH_") || strings.HasPrefix(e, "GITHUB_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh repo create %s: %w: %s", slug, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func confirm(c *cobra.Command, prompt string) (bool, error) {
	fmt.Fprint(c.OutOrStdout(), prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}
