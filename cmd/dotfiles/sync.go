package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/audit"
	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/dlog"
	"github.com/llbbl/dotfiles-manager/internal/snapshot"
	"github.com/llbbl/dotfiles-manager/internal/tracker"
	"github.com/llbbl/dotfiles-manager/internal/vcs"
	"github.com/spf13/cobra"
)

const exitDrift = 6

type syncStrategy string

const (
	stratAuto       syncStrategy = "auto"
	stratKeepLocal  syncStrategy = "keep-local"
	stratKeepRemote syncStrategy = "keep-remote"
	stratAbort      syncStrategy = "abort"
)

type fileChange struct {
	DisplayPath string `json:"display_path"`
	BackupPath  string `json:"backup_path"`
	OldHash     string `json:"old_hash,omitempty"`
	NewHash     string `json:"new_hash"`
	New         bool   `json:"new"`
}

func newSyncCmd() *cobra.Command {
	var (
		dryRun     bool
		message    string
		strategyF  string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Commit and push tracked files to the private backup repo",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			cfg := config.FromContext(ctx)
			if cfg == nil {
				return errors.New("config not loaded")
			}

			strategy := syncStrategy(strings.ToLower(strategyF))
			switch strategy {
			case stratAuto, stratKeepLocal, stratKeepRemote, stratAbort:
			default:
				return fmt.Errorf("invalid --strategy %q (auto|keep-local|keep-remote|abort)", strategyF)
			}

			repo, err := vcs.Open(cfg)
			if err != nil {
				if errors.Is(err, vcs.ErrNotInitialized) {
					fmt.Fprintf(c.ErrOrStderr(),
						"sync: backup repo not initialized at %s; run `dotfiles init`\n",
						cfg.Repo.Local)
					os.Exit(exitInitNoTTY)
				}
				return err
			}

			s, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			if !dryRun {
				if err := repo.Fetch(ctx); err != nil {
					// Fetch can fail if no upstream exists yet — that's fine
					// for a first sync; we will push to create the branch.
					fmt.Fprintf(c.ErrOrStderr(), "warning: fetch failed: %v\n", err)
				}
				ahead, behind, err := repo.AheadBehind(ctx)
				if err == nil && behind > 0 {
					if err := resolveDrift(c, repo, ctx, strategy, ahead, behind); err != nil {
						return err
					}
				}
			}

			mgr, err := newSnapshotManager(ctx, s)
			if err != nil {
				return err
			}

			files, err := tracker.List(ctx, s)
			if err != nil {
				return err
			}

			home, _ := os.UserHomeDir()
			if home != "" {
				if rh, err := filepath.EvalSymlinks(home); err == nil {
					home = rh
				}
			}

			var changes []fileChange
			var newCount, modCount int
			for _, f := range files {
				rep := computeStatusOneInline(f)
				if rep.Status == tracker.StatusMissing {
					continue
				}
				backupRel, err := sanitizeBackupPath(f.Path, home)
				if err != nil {
					return fmt.Errorf("sanitize %s: %w", f.DisplayPath, err)
				}
				data, err := os.ReadFile(f.Path)
				if err != nil {
					return fmt.Errorf("read %s: %w", f.Path, err)
				}
				newHash := rep.Hash
				oldHash := f.LastHash
				isNew := oldHash == "" || oldHash != newHash && !backupExists(repo.Path(), backupRel)
				if oldHash != newHash || !backupExists(repo.Path(), backupRel) {
					if isNew || oldHash == "" {
						newCount++
					} else {
						modCount++
					}
					changes = append(changes, fileChange{
						DisplayPath: f.DisplayPath,
						BackupPath:  backupRel,
						OldHash:     oldHash,
						NewHash:     newHash,
						New:         oldHash == "",
					})
				}
				if dryRun {
					continue
				}
				// Snapshot before writing the backup-side copy.
				file := f
				if _, snapErr := mgr.Snapshot(ctx, f.Path, &file, snapshot.ReasonPreSync); snapErr != nil {
					fmt.Fprintf(c.ErrOrStderr(),
						"warning: pre-sync snapshot of %s failed: %v\n", f.DisplayPath, snapErr)
				}
				if err := repo.WriteFile(backupRel, data); err != nil {
					return err
				}
				// Update tracker's last_synced + last_hash.
				if _, err := s.DB().ExecContext(ctx,
					`UPDATE tracked_files SET last_hash = ?, last_synced = ? WHERE id = ?`,
					newHash, time.Now().UTC().Format(time.RFC3339), f.ID); err != nil {
					return err
				}
			}

			if dryRun {
				if asJSON {
					return jsonEncode(c.OutOrStdout(), map[string]any{
						"dry_run":  true,
						"changes":  changes,
						"modified": modCount,
						"new":      newCount,
					})
				}
				fmt.Fprintf(c.OutOrStdout(), "dry-run: %d changes (%d modified, %d new)\n",
					len(changes), modCount, newCount)
				for _, ch := range changes {
					fmt.Fprintf(c.OutOrStdout(), "  %s -> %s\n", ch.DisplayPath, ch.BackupPath)
				}
				return nil
			}

			// Per-file JSONL records into the backup repo's actions log.
			for _, ch := range changes {
				fields := map[string]any{
					"display_path": ch.DisplayPath,
					"backup_path":  ch.BackupPath,
					"new_hash":     ch.NewHash,
				}
				if ch.OldHash != "" {
					fields["old_hash"] = ch.OldHash
				}
				if ch.New {
					fields["new"] = true
				}
				if err := appendBackupLog(repo, "sync.file", fields); err != nil {
					return err
				}
				if err := auditLog(ctx, "sync.file", fields); err != nil {
					return err
				}
			}

			subject := message
			if subject == "" {
				subject = fmt.Sprintf("sync: %d files (%d modified, %d new)", len(changes), modCount, newCount)
			}
			body := buildCommitBody(changes)
			full := subject
			if body != "" {
				full = subject + "\n\n" + body
			}
			res, err := repo.CommitAll(ctx, full)
			if err != nil {
				return err
			}
			if !res.Empty {
				if err := repo.Push(ctx); err != nil {
					return err
				}
			}

			summary := map[string]any{
				"changes":    len(changes),
				"modified":   modCount,
				"new":        newCount,
				"commit":     res.SHA,
				"empty":      res.Empty,
				"branch":     res.Branch,
			}
			if err := auditLog(ctx, "sync", summary); err != nil {
				return err
			}

			ahead, behind, _ := repo.AheadBehind(ctx)
			dlog.From(ctx).Info("sync run", "files", len(changes), "ahead", ahead, "behind", behind)

			if asJSON {
				return jsonEncode(c.OutOrStdout(), summary)
			}
			if res.Empty {
				fmt.Fprintln(c.OutOrStdout(), "sync: no changes")
				return nil
			}
			shortSHA := res.SHA
			if len(shortSHA) > 8 {
				shortSHA = shortSHA[:8]
			}
			fmt.Fprintf(c.OutOrStdout(), "sync: %d files committed (%s), pushed to origin/%s\n",
				len(changes), shortSHA, res.Branch)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report the plan without writing")
	cmd.Flags().StringVar(&message, "message", "", "override the commit subject line")
	cmd.Flags().StringVar(&strategyF, "strategy", "auto",
		"conflict policy: auto|keep-local|keep-remote|abort")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit summary as JSON")
	return cmd
}

func computeStatusOneInline(f tracker.File) tracker.StatusReport {
	info, err := os.Stat(f.Path)
	if err != nil || info.IsDir() {
		return tracker.StatusReport{File: f, Status: tracker.StatusMissing}
	}
	hash, err := tracker.HashFile(f.Path)
	if err != nil {
		return tracker.StatusReport{File: f, Status: tracker.StatusMissing}
	}
	rep := tracker.StatusReport{File: f, Hash: hash}
	if f.LastHash == "" {
		rep.Status = tracker.StatusNew
	} else if hash == f.LastHash {
		rep.Status = tracker.StatusClean
	} else {
		rep.Status = tracker.StatusModified
	}
	return rep
}

func backupExists(repoPath, rel string) bool {
	_, err := os.Stat(filepath.Join(repoPath, rel))
	return err == nil
}

// sanitizeBackupPath maps a source path to its position inside the backup repo.
func sanitizeBackupPath(absPath, home string) (string, error) {
	cleaned := filepath.Clean(absPath)
	if home != "" {
		if rel, err := filepath.Rel(home, cleaned); err == nil &&
			!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			joined := filepath.Join("files", rel)
			if escapes(joined) {
				return "", fmt.Errorf("path escapes repo: %s", absPath)
			}
			return joined, nil
		}
	}
	stripped := strings.TrimPrefix(cleaned, string(filepath.Separator))
	joined := filepath.Join("files", "_abs", stripped)
	if escapes(joined) {
		return "", fmt.Errorf("path escapes repo: %s", absPath)
	}
	return joined, nil
}

func escapes(p string) bool {
	cleaned := filepath.Clean(p)
	return strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned)
}

// appendBackupLog writes to the durable JSONL trail committed inside
// the private backup repo. This is intentionally independent of
// cfg.Log.Backend — the in-repo log is always written.
func appendBackupLog(repo *vcs.Repo, action string, fields map[string]any) error {
	rec := make(map[string]any, len(fields)+2)
	maps.Copy(rec, fields)
	rec["ts"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	rec["action"] = action
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	return repo.AppendFile(filepath.Join("logs", "actions.jsonl"), line)
}

func auditLog(ctx context.Context, action string, fields map[string]any) error {
	if l := audit.Default(); l != nil {
		return l.Log(ctx, action, fields)
	}
	return nil
}

func buildCommitBody(changes []fileChange) string {
	var b strings.Builder
	for _, ch := range changes {
		short := func(h string) string {
			if len(h) > 8 {
				return h[:8]
			}
			return h
		}
		if ch.OldHash == "" {
			fmt.Fprintf(&b, "- %s: (new) %s\n", ch.DisplayPath, short(ch.NewHash))
		} else {
			fmt.Fprintf(&b, "- %s: %s -> %s\n", ch.DisplayPath, short(ch.OldHash), short(ch.NewHash))
		}
	}
	return b.String()
}

func resolveDrift(c *cobra.Command, repo *vcs.Repo, ctx context.Context, strategy syncStrategy, ahead, behind int) error {
	switch strategy {
	case stratKeepLocal:
		return repo.PushForce(ctx)
	case stratKeepRemote:
		return repo.PullKeepRemote(ctx)
	case stratAbort:
		fmt.Fprintf(c.ErrOrStderr(),
			"sync: drift detected (ahead=%d, behind=%d); aborting per --strategy=abort\n", ahead, behind)
		os.Exit(exitDrift)
	case stratAuto:
		if ahead == 0 {
			return repo.PullFastForward(ctx)
		}
		if !isTTY() {
			fmt.Fprintf(c.ErrOrStderr(),
				"sync: drift detected (ahead=%d, behind=%d) and stdin is not a TTY; pass --strategy\n",
				ahead, behind)
			os.Exit(exitDrift)
		}
		fmt.Fprintf(c.OutOrStdout(),
			"sync: drift (ahead=%d, behind=%d). [l]ocal wins / [r]emote wins / [a]bort: ", ahead, behind)
		r := bufio.NewReader(os.Stdin)
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "l", "local":
			return repo.PushForce(ctx)
		case "r", "remote":
			return repo.PullKeepRemote(ctx)
		default:
			fmt.Fprintln(c.ErrOrStderr(), "sync: aborted")
			os.Exit(exitDrift)
		}
	}
	return nil
}

