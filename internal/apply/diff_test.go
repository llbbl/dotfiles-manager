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
