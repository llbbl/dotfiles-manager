package main

import (
	"errors"
	"testing"
)

func TestResolveLogLevel(t *testing.T) {
	cases := []struct {
		name    string
		verbose bool
		env     string
		want    string
		wantErr bool
	}{
		{name: "default_empty", verbose: false, env: "", want: "error"},
		{name: "env_debug", verbose: false, env: "debug", want: "debug"},
		{name: "env_info", verbose: false, env: "info", want: "info"},
		{name: "env_warn", verbose: false, env: "warn", want: "warn"},
		{name: "env_error", verbose: false, env: "error", want: "error"},
		{name: "env_uppercase", verbose: false, env: "DEBUG", want: "debug"},
		{name: "env_whitespace", verbose: false, env: "  info ", want: "info"},
		{name: "verbose_overrides_env_error", verbose: true, env: "error", want: "debug"},
		{name: "verbose_overrides_empty", verbose: true, env: "", want: "debug"},
		{name: "env_bogus_errors", verbose: false, env: "bogus", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLogLevel(tc.verbose, tc.env)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got level=%q", got)
				}
				var ee *exitError
				if !errors.As(err, &ee) {
					t.Fatalf("want exitError, got %T: %v", err, err)
				}
				if ee.code != exitResolveErr {
					t.Errorf("exit code = %d, want %d", ee.code, exitResolveErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("level = %q, want %q", got, tc.want)
			}
		})
	}
}
