package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/vcs"
	"github.com/spf13/cobra"
)

const (
	exitInitNoTTY = 4
)

// newInitCmd builds the `dfm init` command, which performs
// first-run setup of the private backup repo: cloning an existing
// remote, or initializing a new repo and optionally creating the
// matching private GitHub remote with `gh`. --remote sets the URL,
// --create-remote provisions it via the GitHub API, and --yes skips
// interactive confirmations.
func newInitCmd() *cobra.Command {
	var (
		remoteFlag   string
		createRemote bool
		yes          bool
		tursoFlag    bool
		tursoDBName  string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "First-run setup: clone or create the private backup repo",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg := config.FromContext(c.Context())
			if cfg == nil {
				return errors.New("config not loaded")
			}

			// Determine whether the user asked for the repo flow.
			// Repo flow is requested when --remote, --create-remote, or
			// a non-empty [repo].remote in config is present. --turso
			// alone bypasses the repo flow entirely.
			repoRequested := remoteFlag != "" || createRemote || cfg.Repo.Remote != ""

			runRepoFlow := func() error {
				if remoteFlag != "" {
					cfg.Repo.Remote = remoteFlag
				}
				if cfg.Repo.Remote == "" {
					fmt.Fprintln(c.ErrOrStderr(), "init: --remote (or [repo].remote) is required")
					os.Exit(exitResolveErr)
				}
				if cfg.Repo.Local == "" {
					fmt.Fprintln(c.ErrOrStderr(), "init: [repo].local is empty")
					os.Exit(exitResolveErr)
				}
				return runRepoInit(c, cfg, createRemote, yes)
			}

			runTursoFlow := func() error {
				name := tursoDBName
				if name == "" {
					name = "dotfiles-state"
				}
				path := flagConfigPath
				if path == "" {
					p, err := config.DefaultPath()
					if err != nil {
						return err
					}
					path = p
				}
				return runTursoInit(c.Context(), cfg, path, name)
			}

			// Neither: existing error path.
			if !repoRequested && !tursoFlag {
				fmt.Fprintln(c.ErrOrStderr(), "init: --remote (or [repo].remote) is required")
				os.Exit(exitResolveErr)
			}

			if repoRequested {
				if err := runRepoFlow(); err != nil {
					return err
				}
			}
			if tursoFlag {
				if err := runTursoFlow(); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&remoteFlag, "remote", "", "remote git URL (overrides [repo].remote)")
	cmd.Flags().BoolVar(&createRemote, "create-remote", false, "create the remote via gh if it doesn't exist")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the gh-create confirmation")
	cmd.Flags().BoolVar(&tursoFlag, "turso", false, "set up a Turso libSQL remote DB (orthogonal to repo flow)")
	cmd.Flags().StringVar(&tursoDBName, "turso-db-name", "dotfiles-state", "Turso DB name to create or reuse")
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
