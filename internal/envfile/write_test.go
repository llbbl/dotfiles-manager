package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetKey_ReplacesInPlaceAt0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", ".env")
	if err := SetKey(path, "OTHER", "keep"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := SetKey(path, "SECRET", "one"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := SetKey(path, "SECRET", "two"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if got != "OTHER=keep\nSECRET=two\n" {
		t.Errorf("content = %q", got)
	}
	if n := strings.Count(got, "SECRET="); n != 1 {
		t.Errorf("SECRET= appears %d times, want 1", n)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

// Parse takes the last occurrence of a key, so SetKey has to replace
// every one. A surviving duplicate would keep a rotated-out credential
// live; a surviving "export" form would strand it on disk.
func TestSetKey_ReplacesEveryOccurrenceAndExportForm(t *testing.T) {
	for _, tc := range []struct {
		name, before, want string
	}{
		{
			"present twice",
			"K=old1\nFOO=bar\nK=old2\n",
			"K=NEW\nFOO=bar\nK=NEW\n",
		},
		{
			"export form",
			"export K=old\n",
			"K=NEW\n",
		},
		{
			"export and plain",
			"export K=old1\nFOO=bar\nK=old2\n",
			"K=NEW\nFOO=bar\nK=NEW\n",
		},
		{
			"commented out is not a match",
			"# K=old\n",
			"# K=old\nK=NEW\n",
		},
		{
			"absent appends",
			"FOO=bar\n",
			"FOO=bar\nK=NEW\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".env")
			if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := SetKey(path, "K", "NEW"); err != nil {
				t.Fatalf("SetKey: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("file = %q, want %q", got, tc.want)
			}
			// Whatever the layout, the parser must land on the new value.
			parsed, err := Parse(strings.NewReader(string(got)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if parsed["K"] != "NEW" {
				t.Errorf("Parse resolved K = %q, want NEW", parsed["K"])
			}
		})
	}
}

func TestDeleteKey(t *testing.T) {
	for _, tc := range []struct {
		name, before, want string
		missing, removed   bool
	}{
		{name: "present", before: "OTHER=keep\n export\tTURSO_AUTH_TOKEN=old\nTURSO_AUTH_TOKEN=duplicate\n# untouched\n", want: "OTHER=keep\n# untouched\n", removed: true},
		{name: "key missing", before: "OTHER=keep", want: "OTHER=keep"},
		{name: "file missing", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			removed, err := DeleteKey(path, "TURSO_AUTH_TOKEN")
			if err != nil || removed != tc.removed {
				t.Fatal("unexpected deletion result")
			}
			got, err := os.ReadFile(path)
			if tc.missing {
				if !os.IsNotExist(err) {
					t.Fatal("missing file was created")
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Error("other lines were changed or token retained")
			}
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			wantMode := os.FileMode(0o644)
			if tc.removed {
				wantMode = 0o600
			}
			if st.Mode().Perm() != wantMode {
				t.Error("unexpected file permissions")
			}
		})
	}
}

func TestDeleteKeyParserWhitespace(t *testing.T) {
	for _, assignment := range []string{"TURSO_AUTH_TOKEN =SECRET-110", " export\tTURSO_AUTH_TOKEN \t=SECRET-110", "\tTURSO_AUTH_TOKEN\t = 'SECRET-110'\r"} {
		t.Run(assignment[:strings.IndexByte(assignment, '=')], func(t *testing.T) {
			before := "# keep\r\nOTHER=keep\r\n" + assignment + "\n"
			parsed, err := Parse(strings.NewReader(before))
			if err != nil || parsed["TURSO_AUTH_TOKEN"] != "SECRET-110" {
				t.Fatal("fixture is not a parser-valid token assignment")
			}
			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			removed, err := DeleteKey(path, "TURSO_AUTH_TOKEN")
			if err != nil || !removed {
				t.Fatal("parser-valid token assignment was not removed")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != "# keep\r\nOTHER=keep\r\n" {
				t.Error("token remains or unrelated lines changed")
			}
		})
	}
}
