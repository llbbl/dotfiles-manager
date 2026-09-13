package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinHome points HOME and both XDG roots at a temp dir so nothing here
// can read or write the developer's real config.toml or .env.
func pinHome(t *testing.T) (home string, cfgPath string, envPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return home,
		filepath.Join(home, ".config", "dotfiles", "config.toml"),
		filepath.Join(home, ".local", "share", "dotfiles", ".env")
}

func TestSave_NewFileIs0600(t *testing.T) {
	_, cfgPath, _ := pinHome(t)
	if err := Save(cfgPath, Defaults()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

// os.Create under the default umask produced 0644. Save must narrow an
// existing file rather than preserve whatever bits it found.
func TestSave_Narrows0644To0600(t *testing.T) {
	_, cfgPath, _ := pinHome(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("# stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(cfgPath, Defaults()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

// A blanket blank passes the env-only row and fails the other two: the
// file's own token must survive, in the env file rather than the config.
func TestSaveKeepingFileToken(t *testing.T) {
	for _, tc := range []struct {
		name, fileToken, envToken string
		wantMigrated              bool
	}{
		{"env only", "", "env-secret", false},
		{"file only", "file-secret", "", true},
		{"both", "file-secret", "env-secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cfgPath, envPath := pinHome(t)

			seed := Defaults()
			seed.State.AuthToken = tc.fileToken
			if err := Save(cfgPath, seed); err != nil {
				t.Fatalf("seed config: %v", err)
			}

			t.Setenv("TURSO_AUTH_TOKEN", tc.envToken)
			cfg, err := Load(cfgPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			cfg.State.URL = "libsql://new-db.turso.io"

			migrated, gotEnvPath, err := SaveKeepingFileToken(cfgPath, cfg)
			if err != nil {
				t.Fatalf("SaveKeepingFileToken: %v", err)
			}
			if migrated != tc.wantMigrated {
				t.Errorf("migrated = %v, want %v", migrated, tc.wantMigrated)
			}
			if gotEnvPath != envPath {
				t.Errorf("envPath = %q, want %q", gotEnvPath, envPath)
			}

			body := readString(t, cfgPath)
			if strings.Contains(body, "auth_token") {
				t.Error("config.toml still carries an auth_token key")
			}
			if !strings.Contains(body, "libsql://new-db.turso.io") {
				t.Error("state.url not persisted to config.toml")
			}

			if !tc.wantMigrated {
				// Nothing new should have been written anywhere.
				if _, err := os.Stat(envPath); !os.IsNotExist(err) {
					t.Errorf("env file created for an env-only token (err=%v)", err)
				}
				return
			}
			env := readString(t, envPath)
			if !strings.Contains(env, "TURSO_AUTH_TOKEN="+tc.fileToken) {
				t.Error("file token not migrated to the env file")
			}
			if tc.envToken != "" && strings.Contains(env, tc.envToken) {
				t.Error("env-only token written to the env file")
			}
			st, err := os.Stat(envPath)
			if err != nil {
				t.Fatal(err)
			}
			if mode := st.Mode().Perm(); mode != 0o600 {
				t.Errorf("env file mode = %o, want 0600", mode)
			}
		})
	}
}

// The env file is written first, so a failure there must leave the only
// copy of the token where it was.
func TestSaveKeepingFileToken_EnvWriteFailureKeepsConfig(t *testing.T) {
	home, cfgPath, envPath := pinHome(t)

	seed := Defaults()
	seed.State.AuthToken = "file-secret"
	if err := Save(cfgPath, seed); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	before := readString(t, cfgPath)

	// A regular file where the env file's parent directory belongs makes
	// MkdirAll fail with ENOTDIR.
	if err := os.MkdirAll(filepath.Join(home, ".local", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(envPath), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.State.URL = "libsql://new-db.turso.io"
	if _, _, err := SaveKeepingFileToken(cfgPath, cfg); err == nil {
		t.Fatal("expected an error when the env file cannot be written")
	}
	if after := readString(t, cfgPath); after != before {
		t.Error("config.toml was rewritten despite a failed env write")
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// Nothing in dfm reads the env file the migration writes to — it exists
// for the user's shell. A notice that omits that step leaves a working
// setup broken with no explanation.
func TestTokenMigrationNotice_NamesTheShellStep(t *testing.T) {
	got := TokenMigrationNotice("/tmp/x/.env")
	for _, want := range []string{"/tmp/x/.env", "TURSO_AUTH_TOKEN", "shell"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice does not mention %q:\n%s", want, got)
		}
	}
}
