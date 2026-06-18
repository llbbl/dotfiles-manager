package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// §7 case #11: default tab-separated output.
func TestPathList_DefaultOutput(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	for _, args := range [][]string{
		{"add", "--file", canonical, "/a"},
		{"add", "--file", canonical, "/b"},
		{"add", "--file", canonical, "--append", "/c"},
	} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("seed %v: %v", args, err)
		}
	}

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--file", canonical})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "DIR") || !strings.Contains(got, "DIRECTION") || !strings.Contains(got, "MARKER_ID") {
		t.Fatalf("missing header in:\n%s", got)
	}

	prependID := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	appendID := pathMarkerID(pathDirectionAppend, []string{"/c"})

	lines := strings.Split(strings.TrimSpace(got), "\n")
	// Header + 3 rows.
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4:\n%s", len(lines), got)
	}
	// Row order: /a (prepend) first, then /b (prepend), then /c (append).
	wantOrder := []struct {
		dir, dir2, markerID string
	}{
		{"/a", "prepend", prependID},
		{"/b", "prepend", prependID},
		{"/c", "append", appendID},
	}
	for i, w := range wantOrder {
		ln := lines[i+1]
		if !strings.Contains(ln, w.dir) || !strings.Contains(ln, w.dir2) || !strings.Contains(ln, w.markerID) {
			t.Errorf("row %d = %q, want fields [%s %s %s]", i, ln, w.dir, w.dir2, w.markerID)
		}
	}
}

// §7 case #12: --json output.
func TestPathList_JSON(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	for _, args := range [][]string{
		{"add", "--file", canonical, "/a"},
		{"add", "--file", canonical, "/b"},
		{"add", "--file", canonical, "--append", "/c"},
	} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("seed %v: %v", args, err)
		}
	}

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--file", canonical, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list --json: %v", err)
	}

	var rows []map[string]string
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("invalid json: %v\nout: %s", err, out.String())
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3:\n%s", len(rows), out.String())
	}
	prependID := pathMarkerID(pathDirectionPrepend, []string{"/a", "/b"})
	appendID := pathMarkerID(pathDirectionAppend, []string{"/c"})
	wantRows := []map[string]string{
		{"dir": "/a", "direction": "prepend", "marker_id": prependID},
		{"dir": "/b", "direction": "prepend", "marker_id": prependID},
		{"dir": "/c", "direction": "append", "marker_id": appendID},
	}
	for i, w := range wantRows {
		for k, v := range w {
			if rows[i][k] != v {
				t.Errorf("row %d field %s = %q, want %q", i, k, rows[i][k], v)
			}
		}
	}
}

// Empty rc: tab mode prints nothing (or header-only at most); JSON mode
// emits `[]`. Either way exit 0.
func TestPathList_EmptyFile(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--file", canonical})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list (empty tab): %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("expected empty tab output, got %q", out.String())
	}

	cmd2 := newPathCmd()
	cmd2.SetContext(ctx)
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"list", "--file", canonical, "--json"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("list (empty json): %v", err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(out2.Bytes(), &rows); err != nil {
		t.Fatalf("invalid json: %v\nout: %s", err, out2.String())
	}
	if len(rows) != 0 {
		t.Errorf("expected empty array, got %d rows", len(rows))
	}
}

// Fish rc works the same way: findPathManagedEntries is shell-agnostic.
func TestPathList_FishRc(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTrackedNamed(t, ctx, "config.fish", "")

	for _, args := range [][]string{
		{"add", "--file", canonical, "/a"},
		{"add", "--file", canonical, "--append", "/b"},
	} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--file", canonical, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}

// Marker id in list output matches the per-entry pathMarkerID derived
// from (direction, dirs). Already covered indirectly above, but pin it
// down with an explicit assertion against the deterministic id.
func TestPathList_MarkerIDMatches(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	for _, args := range [][]string{
		{"add", "--file", canonical, "/x"},
		{"add", "--file", canonical, "/y"},
	} {
		cmd := newPathCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--file", canonical, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	wantID := pathMarkerID(pathDirectionPrepend, []string{"/x", "/y"})
	for _, r := range rows {
		if r["marker_id"] != wantID {
			t.Errorf("marker_id = %q, want %q", r["marker_id"], wantID)
		}
	}
}

// --shell + --file together → exitResolveErr.
func TestPathList_ShellAndFileMutuallyExclusive(t *testing.T) {
	ctx, _, _ := setupEditCmdEnv(t)
	canonical, _ := writeTracked(t, ctx, "")

	cmd := newPathCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--shell", "bash", "--file", canonical})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected mutex error, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitResolveErr {
		t.Fatalf("want exitError(code=%d), got %v", exitResolveErr, err)
	}
	if !strings.Contains(ee.msg, "mutually exclusive") {
		t.Errorf("error lacks 'mutually exclusive': %q", ee.msg)
	}
}

