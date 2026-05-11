package secrets

import (
	"bytes"
	"strings"
	"testing"
)

func scanString(t *testing.T, s string) Result {
	t.Helper()
	r, err := ScanReader(strings.NewReader(s))
	if err != nil {
		t.Fatalf("ScanReader: %v", err)
	}
	return r
}

func hasRule(findings []Finding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func TestMask(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"AKIA1234567890ABCDEF", "AKIA…EF"},
		{"short", "•••"},
		{"12345678", "1234…78"},
		{"1234567", "•••"},
		{"", "•••"},
	}
	for _, c := range cases {
		if got := Mask(c.in); got != c.want {
			t.Errorf("Mask(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRule_AWSAccessKey(t *testing.T) {
	pos := scanString(t, "key = AKIAIOSFODNN7EXAMPLE\n")
	if !hasRule(pos.Findings, "aws-access-key") {
		t.Errorf("expected aws-access-key hit, got %+v", pos.Findings)
	}
	for _, f := range pos.Findings {
		if f.Rule == "aws-access-key" && strings.Contains(f.Excerpt, "AKIAIOSFODNN7EXAMPLE") {
			t.Errorf("excerpt was not masked: %q", f.Excerpt)
		}
	}
	neg := scanString(t, "key = AKIA-not-a-real-key\n")
	if hasRule(neg.Findings, "aws-access-key") {
		t.Errorf("did not expect aws-access-key hit: %+v", neg.Findings)
	}
}

func TestRule_AWSSecretKey_RequiresContext(t *testing.T) {
	// 40-char base64-ish run with the context word on same line.
	pos := scanString(t, "aws_secret_access_key=abcdefghijklmnopqrstuvwxyz0123456789ABCD\n")
	if !hasRule(pos.Findings, "aws-secret-key") {
		t.Errorf("expected aws-secret-key hit, got %+v", pos.Findings)
	}
	// Context on previous line.
	posPrev := scanString(t, "aws_secret_access_key:\n   abcdefghijklmnopqrstuvwxyz0123456789ABCD\n")
	if !hasRule(posPrev.Findings, "aws-secret-key") {
		t.Errorf("expected aws-secret-key hit with prev-line context, got %+v", posPrev.Findings)
	}
	// Bare 40-char run without context — should NOT fire.
	neg := scanString(t, "abcdefghijklmnopqrstuvwxyz0123456789ABCD\n")
	if hasRule(neg.Findings, "aws-secret-key") {
		t.Errorf("did not expect aws-secret-key hit without context: %+v", neg.Findings)
	}
}

func TestRule_PrivateKey(t *testing.T) {
	pos := scanString(t, "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----\n")
	if !hasRule(pos.Findings, "private-key") {
		t.Errorf("expected private-key hit, got %+v", pos.Findings)
	}
	neg := scanString(t, "-----BEGIN CERTIFICATE-----\nMIID\n-----END CERTIFICATE-----\n")
	if hasRule(neg.Findings, "private-key") {
		t.Errorf("did not expect private-key hit on cert: %+v", neg.Findings)
	}
}

func TestRule_GitHubToken(t *testing.T) {
	pos := scanString(t, "token=ghp_abcdefghijklmnopqrstuvwxyz0123456789\n")
	if !hasRule(pos.Findings, "github-token") {
		t.Errorf("expected github-token hit, got %+v", pos.Findings)
	}
	neg := scanString(t, "token=ghp_tooshort\n")
	if hasRule(neg.Findings, "github-token") {
		t.Errorf("did not expect github-token hit: %+v", neg.Findings)
	}
}

func TestRule_SlackToken(t *testing.T) {
	pos := scanString(t, "slack=xoxb-1234567890-abcdefg\n")
	if !hasRule(pos.Findings, "slack-token") {
		t.Errorf("expected slack-token hit, got %+v", pos.Findings)
	}
	neg := scanString(t, "slack=xoxz-nope-prefix\n")
	if hasRule(neg.Findings, "slack-token") {
		t.Errorf("did not expect slack-token hit: %+v", neg.Findings)
	}
}

func TestRule_JWT(t *testing.T) {
	pos := scanString(t, "auth: eyJabcdefghij.eyJabcdefghij.signature_part_here\n")
	if !hasRule(pos.Findings, "generic-jwt") {
		t.Errorf("expected generic-jwt hit, got %+v", pos.Findings)
	}
	neg := scanString(t, "auth: eyJshort\n")
	if hasRule(neg.Findings, "generic-jwt") {
		t.Errorf("did not expect generic-jwt hit: %+v", neg.Findings)
	}
}

func TestRule_OpenAIKey(t *testing.T) {
	pos := scanString(t, "OPENAI=sk-abcdefghijklmnopqrstuvwx\n")
	if !hasRule(pos.Findings, "openai-key") {
		t.Errorf("expected openai-key hit, got %+v", pos.Findings)
	}
	neg := scanString(t, "OPENAI=sk-short\n")
	if hasRule(neg.Findings, "openai-key") {
		t.Errorf("did not expect openai-key hit: %+v", neg.Findings)
	}
}

func TestRule_AnthropicKey(t *testing.T) {
	pos := scanString(t, "ANTHROPIC=sk-ant-abcdefghijklmnopqrstuv\n")
	if !hasRule(pos.Findings, "anthropic-key") {
		t.Errorf("expected anthropic-key hit, got %+v", pos.Findings)
	}
	neg := scanString(t, "ANTHROPIC=sk-ant-short\n")
	if hasRule(neg.Findings, "anthropic-key") {
		t.Errorf("did not expect anthropic-key hit: %+v", neg.Findings)
	}
}

func TestRule_EnvLike(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`FOO_TOKEN=abcdefgh12345`, true},
		{`API_KEY=supersecret123`, true},
		{`PASSWORD=hunter2hunter2`, true},
		{`FOO_TOKEN=""`, false},
		{`FOO_TOKEN=''`, false},
		{`FOO_TOKEN=`, false},
		{`FOO_TOKEN=short`, false},
		{`# TOKEN=irrelevant comment`, false},
	}
	for _, c := range cases {
		r := scanString(t, c.line+"\n")
		got := hasRule(r.Findings, "env-like")
		if got != c.want {
			t.Errorf("env-like %q: got=%v want=%v findings=%+v", c.line, got, c.want, r.Findings)
		}
	}
}

func TestBinaryDetection(t *testing.T) {
	data := []byte("hello world\x00 more text\n")
	r, err := ScanReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Skipped || r.Reason != "binary file" {
		t.Errorf("expected binary skip, got %+v", r)
	}
}

func TestSizeLimit(t *testing.T) {
	big := bytes.Repeat([]byte("a"), MaxBytes+1)
	r, err := ScanReader(bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Skipped || r.Reason != "file exceeds 1 MiB" {
		t.Errorf("expected size skip, got skipped=%v reason=%q", r.Skipped, r.Reason)
	}

	// Boundary: exactly MaxBytes must NOT skip.
	ok := bytes.Repeat([]byte("a"), MaxBytes)
	r2, err := ScanReader(bytes.NewReader(ok))
	if err != nil {
		t.Fatal(err)
	}
	if r2.Skipped {
		t.Errorf("did not expect skip at exactly MaxBytes, got %+v", r2)
	}
}

func TestTruncationSentinel(t *testing.T) {
	var b bytes.Buffer
	for i := 0; i < 150; i++ {
		b.WriteString("token=ghp_abcdefghijklmnopqrstuvwxyz0123456789\n")
	}
	r := scanString(t, b.String())
	if len(r.Findings) != maxFindings+1 {
		t.Fatalf("expected %d findings (incl sentinel), got %d", maxFindings+1, len(r.Findings))
	}
	last := r.Findings[len(r.Findings)-1]
	if last.Rule != "truncated" || last.Line != -1 {
		t.Errorf("expected truncation sentinel, got %+v", last)
	}
}

func TestExcerptMasked(t *testing.T) {
	r := scanString(t, "AWS_KEY=AKIAIOSFODNN7EXAMPLE\n")
	if len(r.Findings) == 0 {
		t.Fatal("expected findings")
	}
	for _, f := range r.Findings {
		if strings.Contains(f.Excerpt, "AKIAIOSFODNN7EXAMPLE") {
			t.Errorf("excerpt leaks secret: %q", f.Excerpt)
		}
		if len(f.Excerpt) > 80 {
			t.Errorf("excerpt exceeds 80 chars: %d", len(f.Excerpt))
		}
	}
}
