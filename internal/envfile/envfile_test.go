package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_BasicAndQuoted(t *testing.T) {
	in := strings.NewReader(`
# a comment
FOO=bar
BAZ="hello world"
QUX='literal \n no-escape'
EMPTY=
SPACED   =   value-with-trailing
export EXPORTED=ok
`)
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]string{
		"FOO":      "bar",
		"BAZ":      "hello world",
		"QUX":      `literal \n no-escape`,
		"EMPTY":    "",
		"SPACED":   "value-with-trailing",
		"EXPORTED": "ok",
	}
	for k, v := range want {
		if got := m[k]; got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestParse_DoubleQuotedEscapes(t *testing.T) {
	in := strings.NewReader(`A="line1\nline2"` + "\n" + `B="tab\there"` + "\n" + `C="quote: \"x\""` + "\n" + `D="back\\slash"` + "\n")
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := map[string]string{
		"A": "line1\nline2",
		"B": "tab\there",
		"C": `quote: "x"`,
		"D": `back\slash`,
	}
	for k, v := range cases {
		if got := m[k]; got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestParse_NoInterpolation(t *testing.T) {
	in := strings.NewReader(`FOO="${BAR}"`)
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m["FOO"] != "${BAR}" {
		t.Errorf("FOO = %q, want literal ${BAR}", m["FOO"])
	}
}

func TestParse_MalformedNamesLineNumber(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // substring expected in error
		// forbidden substrings that would indicate the raw line/value
		// content leaked into the error message (potential partial
		// secret disclosure).
		forbidden []string
	}{
		{"no_equals", "FOO=ok\nBAR=ok\nNOEQ_SECRET_TOKEN\n", "line 3", []string{"NOEQ_SECRET_TOKEN"}},
		{"digit_key", "1FOO=bar\n", "line 1", nil},
		{"unterminated_dq", `FOO="oops` + "\n", "line 1", nil},
		{"unterminated_sq", `FOO='oops` + "\n", "line 1", nil},
		{"bad_escape", `FOO="\z"` + "\n", "line 1", nil},
		{"trailing_after_dq", `FOO="x" SECRETTAIL` + "\n", "line 1", []string{"SECRETTAIL"}},
		{"trailing_after_sq", `FOO='x' SECRETTAIL` + "\n", "line 1", []string{"SECRETTAIL"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.in))
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %q does not contain %q", err.Error(), tc.want)
			}
			for _, bad := range tc.forbidden {
				if strings.Contains(err.Error(), bad) {
					t.Errorf("err %q must not contain raw input content %q", err.Error(), bad)
				}
			}
		})
	}
}

func TestParse_TrailingAfterQuotedWording(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"double", `FOO="x" junk` + "\n", "line 1: unexpected trailing content after double-quoted value"},
		{"single", `FOO='x' junk` + "\n", "line 1: unexpected trailing content after single-quoted value"},
		{"missing_eq", "BADKEY\n", "line 1: missing '=' separator"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.in))
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %q does not contain %q", err.Error(), tc.want)
			}
			if tc.name == "missing_eq" && strings.Contains(err.Error(), "BADKEY") {
				t.Errorf("err %q must not contain raw line content", err.Error())
			}
		})
	}
}

func TestLoad_DoesNotClobberPreSetEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	contents := "DFM_TEST_PRESET=fromfile\nDFM_TEST_NEW=newval\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DFM_TEST_PRESET", "fromshell")
	// Ensure NEW is unset for the test.
	_ = os.Unsetenv("DFM_TEST_NEW")
	t.Cleanup(func() { _ = os.Unsetenv("DFM_TEST_NEW") })

	set, skipped, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if set != 1 {
		t.Errorf("set = %d, want 1", set)
	}
	foundSkip := false
	for _, k := range skipped {
		if k == "DFM_TEST_PRESET" {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("expected DFM_TEST_PRESET in skipped, got %v", skipped)
	}
	if v := os.Getenv("DFM_TEST_PRESET"); v != "fromshell" {
		t.Errorf("preset env clobbered: got %q, want fromshell", v)
	}
	if v := os.Getenv("DFM_TEST_NEW"); v != "newval" {
		t.Errorf("new env not set: got %q, want newval", v)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, _, err := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
