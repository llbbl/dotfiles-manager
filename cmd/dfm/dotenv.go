package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/envfile"
)

// dotenvSource records which .env file (if any) was loaded at startup.
// It is "none" by default and set by resolveAndLoadDotenv. Read by
// `dfm config show` so users can confirm the runtime env injection.
var dotenvSource = "none"

// envFileEnvVar is the env var that overrides the [runtime].dotenv
// config setting (but loses to the --dotenv CLI flag).
const envFileEnvVar = "DFM_ENV_FILE"

// resolveAndLoadDotenv applies the precedence chain:
//
//  1. flagNoDotenv (--no-dotenv) → skip entirely.
//  2. flagPath (--dotenv <path>) → explicit; missing = error.
//  3. DFM_ENV_FILE env var → explicit; missing = error.
//  4. [runtime].dotenv = "<path>" → explicit; missing = error.
//  5. [runtime].dotenv = "auto" → check default locations; silent skip.
//  6. "" or "off" → disabled.
//
// On success, returns the resolved path (or "none" when skipped) and
// stores it in the package-level dotenvSource for later display.
func resolveAndLoadDotenv(flagNoDotenv bool, flagPath, envVar, cfgPath string) (string, error) {
	if flagNoDotenv {
		dotenvSource = "none"
		return "none", nil
	}

	// 2. --dotenv flag wins.
	if flagPath != "" {
		return loadExplicit(flagPath, "--dotenv flag")
	}
	// 3. DFM_ENV_FILE env var.
	if envVar != "" {
		return loadExplicit(envVar, envFileEnvVar+" env var")
	}

	// 4-6. Consult [runtime].dotenv.
	rt, err := config.LoadRuntimeOnly(cfgPath)
	if err != nil {
		// A malformed config.toml shouldn't silently swallow the user's
		// dotenv intent — bubble it up.
		return "none", err
	}
	val := rt.Dotenv
	switch val {
	case "", "off":
		dotenvSource = "none"
		return "none", nil
	case "auto":
		return loadAuto()
	default:
		return loadExplicit(val, "[runtime].dotenv")
	}
}

// loadExplicit loads the file at path or returns an exitResolveErr if
// it doesn't exist. originDesc names the source for the error message.
func loadExplicit(path, originDesc string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "none", exitf(exitResolveErr,
				"dotenv: file %q from %s does not exist", path, originDesc)
		}
		return "none", exitf(exitResolveErr,
			"dotenv: stat %q from %s: %v", path, originDesc, err)
	}
	if err := applyDotenv(path); err != nil {
		return "none", err
	}
	dotenvSource = path
	return path, nil
}

// loadAuto tries default locations in order; missing all of them is
// not an error (the "silently skip" branch of the contract).
func loadAuto() (string, error) {
	for _, p := range defaultDotenvCandidates() {
		if _, err := os.Stat(p); err == nil {
			if lerr := applyDotenv(p); lerr != nil {
				return "none", lerr
			}
			dotenvSource = p
			return p, nil
		}
	}
	dotenvSource = "none"
	return "none", nil
}

// defaultDotenvCandidates returns the search order for [runtime].dotenv
// = "auto": $XDG_CONFIG_HOME/dotfiles/.env, then ./.env.
func defaultDotenvCandidates() []string {
	var out []string
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		out = append(out, filepath.Join(x, "dotfiles", ".env"))
	} else if d, err := os.UserConfigDir(); err == nil && d != "" {
		out = append(out, filepath.Join(d, "dotfiles", ".env"))
	}
	out = append(out, ".env")
	return out
}

// applyDotenv warns on loose perms, then loads the file into the
// process environment without clobbering shell-set keys.
func applyDotenv(path string) error {
	if info, err := os.Stat(path); err == nil {
		// Permission bits: anything looser than 0600 (group/world bits set).
		if info.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(os.Stderr,
				"dfm: warning: %s has loose permissions %#o; recommend chmod 600\n",
				path, info.Mode().Perm())
		}
	}
	if _, _, err := envfile.Load(path); err != nil {
		return exitf(exitResolveErr, "dotenv: %v", err)
	}
	return nil
}
