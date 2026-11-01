// Package stateimport copies rows from one dfm state database into
// another. It powers `dfm state import`: lifting tracked_files,
// snapshots, suggestions, and (optionally) actions forward when a
// user wants to migrate from a local SQLite to Turso, or vice
// versa, without orphaning their history.
//
// The package owns no schema knowledge beyond the small allowlist
// of table names below. It assumes the source DB is already
// migrated to a compatible schema; opening with `store.Open` (no
// auto-migration) keeps the source untouched.
package stateimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// AllowedTables is the canonical list of tables this importer
// knows how to copy, in dependency order. tracked_files MUST
// precede snapshots (FK on file_id) and suggestions (FK on
// file_id). actions is independent.
var AllowedTables = []string{
	"tracked_files",
	"snapshots",
	"suggestions",
	"actions",
}

// DefaultTables is what `dfm state import` imports when the user
// doesn't pass --tables. It's the history that matters for
// continuity; suggestions and actions are opt-in because they're
// often noisy.
var DefaultTables = []string{"tracked_files", "snapshots"}

// Options configures one Import run. Source and Target are open
// SQL handles; the caller owns their lifecycle. BlobExists is an
// optional predicate that, for snapshot rows, returns whether the
// referenced blob is present in the target's blob store. When
// non-nil, snapshot rows whose blobs are missing are skipped (and
// counted under SkippedMissingBlob). When nil, snapshot rows are
// imported unconditionally.
type Options struct {
	Source     *sql.DB
	Target     *sql.DB
	Tables     []string
	DryRun     bool
	Replace    bool
	BlobExists func(hash string) bool
}

// TableResult is the per-table outcome of one Import run.
type TableResult struct {
	Table              string
	Imported           int
	SkippedExisting    int
	SkippedMissingBlob int
}

// Result aggregates per-table outcomes and surfaces any warnings
// the importer collected (e.g. "snapshot xyz skipped: blob
// missing"). Errors that abort the run are returned separately.
type Result struct {
	Tables   []TableResult
	Warnings []string
	DryRun   bool
}

// Totals returns the cumulative counts across all tables in r.
func (r Result) Totals() (imported, skippedExisting, skippedMissingBlob int) {
	for _, t := range r.Tables {
		imported += t.Imported
		skippedExisting += t.SkippedExisting
		skippedMissingBlob += t.SkippedMissingBlob
	}
	return
}

// ValidateTables checks that every name in tables is in the
// AllowedTables allowlist and returns them in dependency order.
// Duplicates are deduped. An unknown table is a hard error.
func ValidateTables(tables []string) ([]string, error) {
	if len(tables) == 0 {
		return append([]string{}, DefaultTables...), nil
	}
	allow := map[string]int{}
	for i, t := range AllowedTables {
		allow[t] = i
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := allow[t]; !ok {
			return nil, fmt.Errorf("unknown table %q (allowed: %s)",
				t, strings.Join(AllowedTables, ","))
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return allow[out[i]] < allow[out[j]]
	})
	return out, nil
}

// ParseTablesFlag splits a comma-separated --tables value.
func ParseTablesFlag(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LocalBlobExistsFunc returns a BlobExists implementation that
// checks the on-disk blob store under blobRoot. Used so the
// importer can skip snapshot rows whose content blob isn't
// present locally — importing them would yield an immediately
// broken row.
func LocalBlobExistsFunc(blobRoot string) func(string) bool {
	return func(hash string) bool {
		if len(hash) < 2 {
			return false
		}
		p := blobRoot + string(os.PathSeparator) + hash[:2] + string(os.PathSeparator) + hash
		_, err := os.Stat(p)
		return err == nil
	}
}

// Import copies rows from opts.Source into opts.Target inside one target
// transaction, so a failure leaves the target unchanged. A dry run performs
// the same writes and rolls them back, which keeps its Result identical to a
// real run's.
func Import(ctx context.Context, opts Options) (Result, error) {
	if opts.Source == nil || opts.Target == nil {
		return Result{}, errors.New("source and target must be non-nil")
	}
	tables, err := ValidateTables(opts.Tables)
	if err != nil {
		return Result{}, err
	}

	tx, err := opts.Target.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("begin target transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	r := &run{opts: opts, tx: tx}
	if err := r.mapFileIDs(ctx); err != nil {
		return Result{}, fmt.Errorf("match tracked files by path: %w", err)
	}

	res := Result{DryRun: opts.DryRun}
	for _, table := range tables {
		tr, warnings, err := r.importTable(ctx, table)
		if err != nil {
			return Result{DryRun: opts.DryRun}, fmt.Errorf("import %s: %w", table, err)
		}
		res.Tables = append(res.Tables, tr)
		res.Warnings = append(res.Warnings, warnings...)
	}

	if opts.DryRun {
		if err := tx.Rollback(); err != nil {
			return Result{}, fmt.Errorf("roll back dry run: %w", err)
		}
		return res, nil
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

// run is one Import's state. fileIDs maps a source tracked_files id to the
// target id for the same path; a source id missing from it has no target file.
type run struct {
	opts    Options
	tx      *sql.Tx
	fileIDs map[string]string
}

// mapFileIDs matches tracked files by path, the only identity two
// independently created databases share. It runs even when tracked_files
// isn't imported, so history still links to files the target already tracks.
func (r *run) mapFileIDs(ctx context.Context) error {
	byPath, err := pathIDs(ctx, r.tx)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	src, err := pathIDs(ctx, r.opts.Source)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	r.fileIDs = make(map[string]string, len(src))
	for path, srcID := range src {
		if id, ok := byPath[path]; ok {
			r.fileIDs[srcID] = id
		}
	}
	return nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func pathIDs(ctx context.Context, db queryer) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT path, id FROM tracked_files`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var path, id string
		if err := rows.Scan(&path, &id); err != nil {
			return nil, err
		}
		out[path] = id
	}
	return out, rows.Err()
}

// linkFile rewrites a source file_id to the target's id for the same file.
// ok is false when the source row pointed at a file the target doesn't track.
func (r *run) linkFile(fileID sql.NullString) (linked sql.NullString, ok bool) {
	if !fileID.Valid {
		return fileID, true
	}
	id, ok := r.fileIDs[fileID.String]
	return sql.NullString{String: id, Valid: ok}, ok
}

func (r *run) importTable(ctx context.Context, table string) (TableResult, []string, error) {
	switch table {
	case "tracked_files":
		return r.importTrackedFiles(ctx)
	case "snapshots":
		return r.importSnapshots(ctx)
	case "suggestions":
		return r.importSuggestions(ctx)
	case "actions":
		return r.importActions(ctx)
	default:
		// Unreachable: ValidateTables gates this.
		return TableResult{}, nil, fmt.Errorf("unhandled table %q", table)
	}
}

// rowExists returns true if a row with the given primary-key
// value already exists in the target table.
func rowExists(ctx context.Context, db queryer, table, pkCol string, pk any) (bool, error) {
	q := fmt.Sprintf(`SELECT 1 FROM %s WHERE %s = ? LIMIT 1`, table, pkCol)
	var one int
	err := db.QueryRowContext(ctx, q, pk).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func (r *run) importTrackedFiles(ctx context.Context) (TableResult, []string, error) {
	res := TableResult{Table: "tracked_files"}
	rows, err := r.opts.Source.QueryContext(ctx,
		`SELECT id, path, display_path, added_at, last_hash, last_synced FROM tracked_files`)
	if err != nil {
		return res, nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id          string
			path        string
			displayPath string
			addedAt     string
			lastHash    sql.NullString
			lastSynced  sql.NullString
		)
		if err := rows.Scan(&id, &path, &displayPath, &addedAt, &lastHash, &lastSynced); err != nil {
			return res, nil, fmt.Errorf("scan: %w", err)
		}
		targetID, matched := r.fileIDs[id]
		switch {
		case matched && !r.opts.Replace:
			res.SkippedExisting++
			continue
		case matched:
			// Update in place: replacing the row would change its id and
			// unlink the target's own history.
			if _, err := r.tx.ExecContext(ctx,
				`UPDATE tracked_files SET display_path = ?, added_at = ?, last_hash = ?, last_synced = ?
				 WHERE id = ?`,
				displayPath, addedAt, lastHash, lastSynced, targetID); err != nil {
				return res, nil, fmt.Errorf("update id=%s: %w", targetID, err)
			}
		default:
			if _, err := r.tx.ExecContext(ctx,
				`INSERT INTO tracked_files (id, path, display_path, added_at, last_hash, last_synced)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				id, path, displayPath, addedAt, lastHash, lastSynced); err != nil {
				return res, nil, fmt.Errorf("insert id=%s: %w", id, err)
			}
			r.fileIDs[id] = id
		}
		res.Imported++
	}
	return res, nil, rows.Err()
}

func (r *run) importSnapshots(ctx context.Context) (TableResult, []string, error) {
	res := TableResult{Table: "snapshots"}
	var warnings []string
	rows, err := r.opts.Source.QueryContext(ctx,
		`SELECT id, file_id, path, hash, size, reason, created_at, storage_path FROM snapshots`)
	if err != nil {
		return res, nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id          string
			fileID      sql.NullString
			path        string
			hash        string
			size        int64
			reason      string
			createdAt   string
			storagePath string
		)
		if err := rows.Scan(&id, &fileID, &path, &hash, &size, &reason, &createdAt, &storagePath); err != nil {
			return res, nil, fmt.Errorf("scan: %w", err)
		}
		if r.opts.BlobExists != nil && !r.opts.BlobExists(hash) {
			res.SkippedMissingBlob++
			warnings = append(warnings,
				fmt.Sprintf("snapshot %s: blob %s not in local store; skipped", id, shortHash(hash)))
			continue
		}
		exists, err := rowExists(ctx, r.tx, "snapshots", "id", id)
		if err != nil {
			return res, nil, fmt.Errorf("check existing id=%s: %w", id, err)
		}
		if exists && !r.opts.Replace {
			res.SkippedExisting++
			continue
		}
		fileID, ok := r.linkFile(fileID)
		if !ok {
			warnings = append(warnings,
				fmt.Sprintf("snapshot %s: %s is not tracked in the target; imported without a file link", id, path))
		}
		verb := "INSERT"
		if exists {
			verb = "INSERT OR REPLACE"
		}
		if _, err := r.tx.ExecContext(ctx,
			verb+` INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, fileID, path, hash, size, reason, createdAt, storagePath); err != nil {
			return res, nil, fmt.Errorf("insert id=%s: %w", id, err)
		}
		res.Imported++
	}
	return res, warnings, rows.Err()
}

func (r *run) importSuggestions(ctx context.Context) (TableResult, []string, error) {
	res := TableResult{Table: "suggestions"}
	var warnings []string
	rows, err := r.opts.Source.QueryContext(ctx,
		`SELECT id, file_id, provider, prompt, diff, status, created_at, decided_at FROM suggestions`)
	if err != nil {
		return res, nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id        string
			fileID    sql.NullString
			provider  string
			prompt    string
			diff      string
			status    string
			createdAt string
			decidedAt sql.NullString
		)
		if err := rows.Scan(&id, &fileID, &provider, &prompt, &diff, &status, &createdAt, &decidedAt); err != nil {
			return res, nil, fmt.Errorf("scan: %w", err)
		}
		exists, err := rowExists(ctx, r.tx, "suggestions", "id", id)
		if err != nil {
			return res, nil, fmt.Errorf("check existing id=%s: %w", id, err)
		}
		if exists && !r.opts.Replace {
			res.SkippedExisting++
			continue
		}
		fileID, ok := r.linkFile(fileID)
		if !ok {
			warnings = append(warnings,
				fmt.Sprintf("suggestion %s: its file is not tracked in the target; imported without a file link", id))
		}
		verb := "INSERT"
		if exists {
			verb = "INSERT OR REPLACE"
		}
		if _, err := r.tx.ExecContext(ctx,
			verb+` INTO suggestions (id, file_id, provider, prompt, diff, status, created_at, decided_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, fileID, provider, prompt, diff, status, createdAt, decidedAt); err != nil {
			return res, nil, fmt.Errorf("insert id=%s: %w", id, err)
		}
		res.Imported++
	}
	return res, warnings, rows.Err()
}

func (r *run) importActions(ctx context.Context) (TableResult, []string, error) {
	res := TableResult{Table: "actions"}
	rows, err := r.opts.Source.QueryContext(ctx,
		`SELECT id, ts, action, payload_json FROM actions`)
	if err != nil {
		return res, nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id          int64
			ts          string
			action      string
			payloadJSON string
		)
		if err := rows.Scan(&id, &ts, &action, &payloadJSON); err != nil {
			return res, nil, fmt.Errorf("scan: %w", err)
		}
		exists, err := rowExists(ctx, r.tx, "actions", "id", id)
		if err != nil {
			return res, nil, fmt.Errorf("check existing id=%d: %w", id, err)
		}
		if exists && !r.opts.Replace {
			res.SkippedExisting++
			continue
		}
		verb := "INSERT"
		if exists {
			verb = "INSERT OR REPLACE"
		}
		if _, err := r.tx.ExecContext(ctx,
			verb+` INTO actions (id, ts, action, payload_json) VALUES (?, ?, ?, ?)`,
			id, ts, action, payloadJSON); err != nil {
			return res, nil, fmt.Errorf("insert id=%d: %w", id, err)
		}
		res.Imported++
	}
	return res, nil, rows.Err()
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
