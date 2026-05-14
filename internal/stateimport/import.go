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

// Import copies rows from opts.Source into opts.Target.
func Import(ctx context.Context, opts Options) (Result, error) {
	if opts.Source == nil || opts.Target == nil {
		return Result{}, errors.New("source and target must be non-nil")
	}
	tables, err := ValidateTables(opts.Tables)
	if err != nil {
		return Result{}, err
	}

	res := Result{DryRun: opts.DryRun}
	for _, table := range tables {
		tr, warnings, err := importTable(ctx, opts, table)
		if err != nil {
			return res, fmt.Errorf("import %s: %w", table, err)
		}
		res.Tables = append(res.Tables, tr)
		res.Warnings = append(res.Warnings, warnings...)
	}
	return res, nil
}

// importTable copies one table. Each known table has its own
// hand-written SELECT/INSERT pair so we know which column is the
// primary key and (for snapshots) which column holds the blob
// hash. Anything outside the AllowedTables list never reaches
// this function.
func importTable(ctx context.Context, opts Options, table string) (TableResult, []string, error) {
	switch table {
	case "tracked_files":
		return importTrackedFiles(ctx, opts)
	case "snapshots":
		return importSnapshots(ctx, opts)
	case "suggestions":
		return importSuggestions(ctx, opts)
	case "actions":
		return importActions(ctx, opts)
	default:
		// Unreachable: ValidateTables gates this.
		return TableResult{}, nil, fmt.Errorf("unhandled table %q", table)
	}
}

// rowExists returns true if a row with the given primary-key
// value already exists in the target table.
func rowExists(ctx context.Context, db *sql.DB, table, pkCol string, pk any) (bool, error) {
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

func importTrackedFiles(ctx context.Context, opts Options) (TableResult, []string, error) {
	res := TableResult{Table: "tracked_files"}
	rows, err := opts.Source.QueryContext(ctx,
		`SELECT id, path, display_path, added_at, last_hash, last_synced FROM tracked_files`)
	if err != nil {
		return res, nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id          int64
			path        string
			displayPath string
			addedAt     string
			lastHash    sql.NullString
			lastSynced  sql.NullString
		)
		if err := rows.Scan(&id, &path, &displayPath, &addedAt, &lastHash, &lastSynced); err != nil {
			return res, nil, fmt.Errorf("scan: %w", err)
		}
		exists, err := rowExists(ctx, opts.Target, "tracked_files", "id", id)
		if err != nil {
			return res, nil, fmt.Errorf("check existing id=%d: %w", id, err)
		}
		if exists && !opts.Replace {
			res.SkippedExisting++
			continue
		}
		if opts.DryRun {
			res.Imported++
			continue
		}
		verb := "INSERT"
		if exists {
			verb = "INSERT OR REPLACE"
		}
		if _, err := opts.Target.ExecContext(ctx,
			verb+` INTO tracked_files (id, path, display_path, added_at, last_hash, last_synced)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, path, displayPath, addedAt, lastHash, lastSynced); err != nil {
			return res, nil, fmt.Errorf("insert id=%d: %w", id, err)
		}
		res.Imported++
	}
	return res, nil, rows.Err()
}

func importSnapshots(ctx context.Context, opts Options) (TableResult, []string, error) {
	res := TableResult{Table: "snapshots"}
	var warnings []string
	rows, err := opts.Source.QueryContext(ctx,
		`SELECT id, file_id, path, hash, size, reason, created_at, storage_path FROM snapshots`)
	if err != nil {
		return res, nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id          string
			fileID      sql.NullInt64
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
		if opts.BlobExists != nil && !opts.BlobExists(hash) {
			res.SkippedMissingBlob++
			warnings = append(warnings,
				fmt.Sprintf("snapshot %s: blob %s not in local store; skipped", id, shortHash(hash)))
			continue
		}
		exists, err := rowExists(ctx, opts.Target, "snapshots", "id", id)
		if err != nil {
			return res, nil, fmt.Errorf("check existing id=%s: %w", id, err)
		}
		if exists && !opts.Replace {
			res.SkippedExisting++
			continue
		}
		if opts.DryRun {
			res.Imported++
			continue
		}
		verb := "INSERT"
		if exists {
			verb = "INSERT OR REPLACE"
		}
		if _, err := opts.Target.ExecContext(ctx,
			verb+` INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, fileID, path, hash, size, reason, createdAt, storagePath); err != nil {
			return res, nil, fmt.Errorf("insert id=%s: %w", id, err)
		}
		res.Imported++
	}
	return res, warnings, rows.Err()
}

func importSuggestions(ctx context.Context, opts Options) (TableResult, []string, error) {
	res := TableResult{Table: "suggestions"}
	rows, err := opts.Source.QueryContext(ctx,
		`SELECT id, file_id, provider, prompt, diff, status, created_at, decided_at FROM suggestions`)
	if err != nil {
		return res, nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id        string
			fileID    sql.NullInt64
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
		exists, err := rowExists(ctx, opts.Target, "suggestions", "id", id)
		if err != nil {
			return res, nil, fmt.Errorf("check existing id=%s: %w", id, err)
		}
		if exists && !opts.Replace {
			res.SkippedExisting++
			continue
		}
		if opts.DryRun {
			res.Imported++
			continue
		}
		verb := "INSERT"
		if exists {
			verb = "INSERT OR REPLACE"
		}
		if _, err := opts.Target.ExecContext(ctx,
			verb+` INTO suggestions (id, file_id, provider, prompt, diff, status, created_at, decided_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, fileID, provider, prompt, diff, status, createdAt, decidedAt); err != nil {
			return res, nil, fmt.Errorf("insert id=%s: %w", id, err)
		}
		res.Imported++
	}
	return res, nil, rows.Err()
}

func importActions(ctx context.Context, opts Options) (TableResult, []string, error) {
	res := TableResult{Table: "actions"}
	rows, err := opts.Source.QueryContext(ctx,
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
		exists, err := rowExists(ctx, opts.Target, "actions", "id", id)
		if err != nil {
			return res, nil, fmt.Errorf("check existing id=%d: %w", id, err)
		}
		if exists && !opts.Replace {
			res.SkippedExisting++
			continue
		}
		if opts.DryRun {
			res.Imported++
			continue
		}
		verb := "INSERT"
		if exists {
			verb = "INSERT OR REPLACE"
		}
		if _, err := opts.Target.ExecContext(ctx,
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
