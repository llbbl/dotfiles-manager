package lsmsecret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func stub(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("PATH", dir)
	log, input := filepath.Join(dir, "args"), filepath.Join(dir, "input")
	t.Setenv("STUB_ARGS", log)
	t.Setenv("STUB_INPUT", input)
	t.Setenv("STUB_MODE", "")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$STUB_ARGS"
printf 'LEAK-STDERR' >&2
case "$STUB_MODE" in
 fail) printf 'LEAK-STDOUT'; exit 1;;
 empty) exit 0;;
 sleep) exec /bin/sleep 5;;
esac
if [ "$1" = set ]; then /bin/cat > "$STUB_INPUT"; printf 'LEAK-STDOUT'; else printf 'SECRET-110\n\n'; fi
`
	if err := os.WriteFile(filepath.Join(dir, "lsm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return log, input
}

func TestSet(t *testing.T) {
	log, input := stub(t)
	ref := config.SecretRef{Provider: "lsm", App: "my-app", Env: "test", Key: "CUSTOM"}
	if err := Set(context.Background(), ref, "SECRET-110"); err != nil {
		t.Fatal("set failed")
	}
	got, err := os.ReadFile(input)
	if err != nil || string(got) != "SECRET-110" {
		t.Error("stdin did not contain exactly the token")
	}
	args, err := os.ReadFile(log)
	if err != nil || string(args) != "set\n--app\nmy-app\n--env\ntest\nCUSTOM\n-\n" {
		t.Error("unexpected set argv")
	}
	if strings.Contains(string(args), "SECRET-110") {
		t.Error("token reached argv")
	}
}

func TestGet(t *testing.T) {
	log, _ := stub(t)
	got, err := Get(context.Background(), config.SecretRef{App: "dfm", Env: "prod"})
	if err != nil || got != "SECRET-110\n\n" {
		t.Fatal("get did not return exact stdout")
	}
	args, err := os.ReadFile(log)
	if err != nil || string(args) != "get\n--app\ndfm\n--env\nprod\nTURSO_AUTH_TOKEN\n" {
		t.Error("unexpected get argv")
	}
}

func TestGetFailures(t *testing.T) {
	for _, mode := range []string{"fail", "empty", "missing", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			stub(t)
			t.Setenv("STUB_MODE", mode)
			if mode == "missing" {
				t.Setenv("PATH", t.TempDir())
			}
			ctx := context.Background()
			if mode == "sleep" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			got, err := Get(ctx, config.SecretRef{App: "dfm", Env: "prod"})
			if err == nil || got != "" {
				t.Fatal("expected empty result and safe error")
			}
			if strings.Contains(err.Error(), "LEAK-") {
				t.Error("subprocess output reached error")
			}
			switch mode {
			case "sleep":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Error("expected timeout")
				}
			case "missing":
				if err.Error() != "lsm not found on PATH ([state.secret] needs it)" {
					t.Error("unexpected missing-binary error")
				}
			case "empty":
				if err.Error() != "lsm returned an empty value for TURSO_AUTH_TOKEN" {
					t.Error("unexpected empty-value error")
				}
			case "fail":
				if err.Error() != "lsm lookup for TURSO_AUTH_TOKEN (app dfm, env prod) failed; run 'lsm get --app … --env … TURSO_AUTH_TOKEN' to see why" {
					t.Error("unexpected lookup error")
				}
			}
		})
	}
}

func TestSetFailure(t *testing.T) {
	stub(t)
	t.Setenv("STUB_MODE", "fail")
	err := Set(context.Background(), config.SecretRef{App: "dfm", Env: "prod"}, "SECRET-110")
	if err == nil {
		t.Fatal("expected set failure")
	}
	if strings.Contains(err.Error(), "LEAK-") || strings.Contains(err.Error(), "SECRET-110") {
		t.Error("set error contains sensitive output")
	}
}

func TestDescendantPipeDeadline(t *testing.T) {
	for _, op := range []string{"get", "set"} {
		for _, child := range []string{"foreground", "background"} {
			t.Run(op+"/"+child, func(t *testing.T) {
				stub(t)
				script := "#!/bin/sh\n/bin/sleep 3\nexit 0\n"
				if child == "background" {
					script = "#!/bin/sh\n/bin/sleep 3 &\nexit 0\n"
				}
				if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "lsm"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				start := time.Now()
				ref := config.SecretRef{Provider: "lsm", App: "dfm", Env: "test"}
				var err error
				if op == "get" {
					_, err = Get(ctx, ref)
				} else {
					err = Set(ctx, ref, "SECRET-110")
				}
				if err == nil {
					t.Error("expected subprocess deadline or pipe-wait error")
				}
				if elapsed := time.Since(start); elapsed > time.Second {
					t.Errorf("descendant pipes delayed return for %s", elapsed)
				}
			})
		}
	}
}
