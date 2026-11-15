package lsmsecret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func Get(ctx context.Context, ref config.SecretRef) (string, error) {
	var out bytes.Buffer
	if err := run(ctx, ref, "get", "", &out); err != nil {
		return "", err
	}
	if out.Len() == 0 {
		return "", fmt.Errorf("lsm returned an empty value for %s", key(ref))
	}
	return out.String(), nil
}

func Set(ctx context.Context, ref config.SecretRef, token string) error {
	return run(ctx, ref, "set", token, io.Discard)
}

func key(ref config.SecretRef) string {
	if ref.Key == "" {
		return "TURSO_AUTH_TOKEN"
	}
	return ref.Key
}

func run(ctx context.Context, ref config.SecretRef, op, token string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{op, "--app", ref.App, "--env", ref.Env, key(ref)}
	if op == "set" {
		args = append(args, "-")
	}
	cmd := exec.CommandContext(ctx, "lsm", args...)
	cmd.WaitDelay = 100 * time.Millisecond
	if op == "set" {
		cmd.Stdin = strings.NewReader(token)
	}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("lsm not found on PATH ([state.secret] needs it)")
		}
		// lsm v0.12.0 documents these exit codes for get only; older lsm exits 1 and
		// falls through to the generic message.
		var exitErr *exec.ExitError
		if op == "get" && errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 2:
				return fmt.Errorf("lsm has no %s for app %s, env %s; set it with 'lsm set --app %s --env %s %s'", key(ref), ref.App, ref.Env, ref.App, ref.Env, key(ref))
			case 3:
				return fmt.Errorf("lsm has no store for app %s, env %s; set the key with 'lsm set --app %s --env %s %s'", ref.App, ref.Env, ref.App, ref.Env, key(ref))
			case 4:
				return errors.New("lsm is not initialised; run 'lsm init'")
			case 5:
				return fmt.Errorf("lsm could not resolve app %s, env %s", ref.App, ref.Env)
			}
		}
		return fmt.Errorf("lsm lookup for %s (app %s, env %s) failed; run 'lsm get --app … --env … %s' to see why", key(ref), ref.App, ref.Env, key(ref))
	}
	return nil
}
