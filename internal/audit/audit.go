// Package audit is a placeholder for the JSONL action log. M4 will replace
// this stub with a real logger. Until then, Log is a no-op so callers can
// declare their audit events now without churning later.
package audit

import "context"

// Log records an event with structured fields. Currently a no-op.
func Log(ctx context.Context, event string, fields map[string]any) {
	_ = ctx
	_ = event
	_ = fields
}
