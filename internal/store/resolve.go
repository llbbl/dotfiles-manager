package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrIDNotFound means no row's id equals or starts with the prefix.
var ErrIDNotFound = errors.New("no id matches prefix")

// AmbiguousIDError reports a prefix that matches more than one id.
type AmbiguousIDError struct {
	Prefix     string
	Candidates []string
}

func (e *AmbiguousIDError) Error() string {
	return fmt.Sprintf("id prefix %q matches %d ids: %s",
		e.Prefix, len(e.Candidates), strings.Join(e.Candidates, ", "))
}

// ResolveID maps a full id or an unambiguous id prefix to the full id.
// table is interpolated into the SQL, so it must always be a caller-supplied
// literal ("snapshots", "suggestions") and never user input.
func (s *Store) ResolveID(ctx context.Context, table, prefix string) (string, error) {
	if prefix == "" {
		return "", ErrIDNotFound
	}

	var exact string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM `+table+` WHERE id = ?`, prefix).Scan(&exact)
	if err == nil {
		return exact, nil
	}
	if !isNoRows(err) {
		return "", fmt.Errorf("lookup id in %s: %w", table, err)
	}

	// substr rather than LIKE: a user-supplied prefix containing % or _
	// would otherwise need escaping.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM `+table+` WHERE substr(id, 1, ?) = ? ORDER BY id`,
		len(prefix), prefix)
	if err != nil {
		return "", fmt.Errorf("scan id prefix in %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("scan id prefix in %s: %w", table, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("scan id prefix in %s: %w", table, err)
	}

	switch len(ids) {
	case 0:
		return "", ErrIDNotFound
	case 1:
		return ids[0], nil
	default:
		return "", &AmbiguousIDError{Prefix: prefix, Candidates: ids}
	}
}

// isNoRows also matches the driver's string-only no-rows error, which
// does not wrap sql.ErrNoRows.
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows")
}
