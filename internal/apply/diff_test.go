package apply

import (
	"errors"
	"strings"
	"testing"
)

func TestApplyToBytes_HappyPath(t *testing.T) {
	orig := []byte("# fixture\nfoo=bar\n")
	diff := "--- a/fixture.txt\n+++ b/fixture.txt\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=\"bar\"\n"
	fs, err := Validate(diff)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	out, hunks, err := ApplyToBytes(orig, fs)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if hunks != 1 {
		t.Errorf("hunks = %d, want 1", hunks)
	}
	want := "# fixture\nfoo=\"bar\"\n"
	if string(out) != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestApplyToBytes_OffsetTolerated(t *testing.T) {
	orig := []byte("\n\n# fixture\nfoo=bar\n")
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=baz\n"
	fs, err := Validate(diff)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	out, _, err := ApplyToBytes(orig, fs)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := "\n\n# fixture\nfoo=baz\n"
	if string(out) != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestApplyToBytes_OffsetExhausted(t *testing.T) {
	orig := []byte(strings.Repeat("\n", 10) + "# fixture\nfoo=bar\n")
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=baz\n"
	fs, err := Validate(diff)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, _, err := ApplyToBytes(orig, fs); !errors.Is(err, ErrDiffDoesNotApply) {
		t.Fatalf("want ErrDiffDoesNotApply, got %v", err)
	}
}

func TestApplyToBytes_ContextMismatch(t *testing.T) {
	orig := []byte("# DIFFERENT\nfoo=bar\n")
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=baz\n"
	fs, err := Validate(diff)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, _, err := ApplyToBytes(orig, fs); !errors.Is(err, ErrDiffDoesNotApply) {
		t.Fatalf("want ErrDiffDoesNotApply, got %v", err)
	}
}

func TestValidate_MultiFileRejected(t *testing.T) {
	diff := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-c\n+d\n"
	if _, err := Validate(diff); !errors.Is(err, ErrDiffMalformed) {
		t.Fatalf("want ErrDiffMalformed, got %v", err)
	}
}

func TestValidate_Empty(t *testing.T) {
	if _, err := Validate(""); !errors.Is(err, ErrDiffEmpty) {
		t.Fatalf("want ErrDiffEmpty, got %v", err)
	}
	if _, err := Validate("   \n  \n"); !errors.Is(err, ErrDiffEmpty) {
		t.Fatalf("want ErrDiffEmpty, got %v", err)
	}
}

func TestApplyToBytes_NoTrailingNewlineOriginal(t *testing.T) {
	// File has no trailing newline; diff matches as-is and keeps the
	// file without one.
	orig := []byte("foo=bar")
	diff := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-foo=bar\n\\ No newline at end of file\n+foo=baz\n\\ No newline at end of file\n"
	fs, err := Validate(diff)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	out, _, err := ApplyToBytes(orig, fs)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(out) != "foo=baz" {
		t.Errorf("out = %q", out)
	}
}

func TestApplyToBytes_DiffAddsTrailingNewline(t *testing.T) {
	orig := []byte("foo=bar")
	diff := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-foo=bar\n\\ No newline at end of file\n+foo=bar\n"
	fs, err := Validate(diff)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	out, _, err := ApplyToBytes(orig, fs)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(out) != "foo=bar\n" {
		t.Errorf("out = %q", out)
	}
}

func TestApplyToBytes_DiffRemovesTrailingNewline(t *testing.T) {
	orig := []byte("foo=bar\n")
	diff := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-foo=bar\n+foo=bar\n\\ No newline at end of file\n"
	fs, err := Validate(diff)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	out, _, err := ApplyToBytes(orig, fs)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(out) != "foo=bar" {
		t.Errorf("out = %q", out)
	}
}

func TestValidate_BareHunkHeaderRejected(t *testing.T) {
	// Validate (strict) MUST continue to reject bare `@@` so that
	// `dfm suggest` never writes a diff that depends on source-byte
	// resolution into the suggestions table.
	cases := []string{
		"--- a/x\n+++ b/x\n@@\n # fixture\n-foo=bar\n+foo=baz\n",
		"--- a/x\n+++ b/x\n@@ @@\n # fixture\n-foo=bar\n+foo=baz\n",
	}
	for _, diff := range cases {
		if _, err := Validate(diff); !errors.Is(err, ErrDiffMalformed) {
			t.Fatalf("Validate(%q): want ErrDiffMalformed, got %v", diff, err)
		}
		if _, err := Parse(diff); !errors.Is(err, ErrDiffMalformed) {
			t.Fatalf("Parse(%q): want ErrDiffMalformed, got %v", diff, err)
		}
	}
}

func TestParseTolerant_BareHunkHeaderAccepted(t *testing.T) {
	diff := "--- a/x\n+++ b/x\n@@\n # fixture\n-foo=bar\n+foo=baz\n"
	fs, err := ParseTolerant(diff)
	if err != nil {
		t.Fatalf("ParseTolerant: %v", err)
	}
	if len(fs.Hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(fs.Hunks))
	}
	if fs.Hunks[0].OldStart != 0 {
		t.Errorf("OldStart = %d, want 0 (sentinel)", fs.Hunks[0].OldStart)
	}
	if got := len(fs.Hunks[0].Lines); got != 3 {
		t.Errorf("lines = %d, want 3", got)
	}
}

func TestResolveSentinelHunks_UniqueAnchor(t *testing.T) {
	orig := []byte("alpha\nbeta\n# fixture\nfoo=bar\ngamma\n")
	diff := "--- a/x\n+++ b/x\n@@\n # fixture\n-foo=bar\n+foo=baz\n"
	fs, err := ParseTolerant(diff)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := ResolveSentinelHunks(&fs, orig); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	h := fs.Hunks[0]
	if h.OldStart != 3 {
		t.Errorf("OldStart = %d, want 3", h.OldStart)
	}
	if h.OldCount != 2 {
		t.Errorf("OldCount = %d, want 2", h.OldCount)
	}
	if h.NewStart != 3 {
		t.Errorf("NewStart = %d, want 3", h.NewStart)
	}
	if h.NewCount != 2 {
		t.Errorf("NewCount = %d, want 2", h.NewCount)
	}

	// End-to-end through ApplyToBytes.
	out, hunks, err := ApplyToBytes(orig, fs)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if hunks != 1 {
		t.Errorf("hunks = %d", hunks)
	}
	want := "alpha\nbeta\n# fixture\nfoo=baz\ngamma\n"
	if string(out) != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestResolveSentinelHunks_AmbiguousMatchNamesLines(t *testing.T) {
	// Two identical anchor regions — must error and name both line
	// numbers.
	orig := []byte("# fixture\nfoo=bar\n# divider\n# fixture\nfoo=bar\n")
	diff := "--- a/x\n+++ b/x\n@@\n # fixture\n-foo=bar\n+foo=baz\n"
	fs, err := ParseTolerant(diff)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = ResolveSentinelHunks(&fs, orig)
	if !errors.Is(err, ErrDiffDoesNotApply) {
		t.Fatalf("want ErrDiffDoesNotApply, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "1") || !strings.Contains(msg, "4") {
		t.Errorf("error %q does not name candidate lines 1 and 4", msg)
	}
}

func TestResolveSentinelHunks_NoMatch(t *testing.T) {
	orig := []byte("alpha\nbeta\ngamma\n")
	diff := "--- a/x\n+++ b/x\n@@\n # fixture\n-foo=bar\n+foo=baz\n"
	fs, err := ParseTolerant(diff)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := ResolveSentinelHunks(&fs, orig); !errors.Is(err, ErrDiffDoesNotApply) {
		t.Fatalf("want ErrDiffDoesNotApply, got %v", err)
	}
}

func TestParseTolerant_WellFormedUnchanged(t *testing.T) {
	// Existing well-formed `@@ -N,M +N,M @@` hunks must parse the same
	// way through the tolerant path.
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=baz\n"
	strict, err := Parse(diff)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tol, err := ParseTolerant(diff)
	if err != nil {
		t.Fatalf("ParseTolerant: %v", err)
	}
	if strict.Hunks[0].OldStart != tol.Hunks[0].OldStart ||
		strict.Hunks[0].OldCount != tol.Hunks[0].OldCount ||
		strict.Hunks[0].NewStart != tol.Hunks[0].NewStart ||
		strict.Hunks[0].NewCount != tol.Hunks[0].NewCount {
		t.Errorf("strict vs tolerant disagree: %+v vs %+v", strict.Hunks[0], tol.Hunks[0])
	}
}

func TestApplyToBytes_CRLFPreserved(t *testing.T) {
	orig := []byte("# fixture\r\nfoo=bar\r\n")
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n # fixture\n-foo=bar\n+foo=baz\n"
	fs, err := Validate(diff)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	out, _, err := ApplyToBytes(orig, fs)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := "# fixture\r\nfoo=baz\r\n"
	if string(out) != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}
