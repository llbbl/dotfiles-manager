package main

import (
	"encoding/json"
	"io"
)

// writeJSON pretty-encodes v as JSON to w with two-space indentation.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
