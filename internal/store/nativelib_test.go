package store

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

// Child processes are needed because the native library loads once per process.
const childModeEnv = "DFM_STORE_TEST_CHILD"

func TestMain(m *testing.M) {
	switch os.Getenv(childModeEnv) {
	case "":
		os.Exit(m.Run())
	case "open":
		os.Exit(childOpen())
	case "hold":
		os.Exit(childHold())
	}
	fmt.Fprintln(os.Stderr, "unknown child mode")
	os.Exit(2)
}

func childHold() int {
	dir := tursoCacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	unlock, err := lockFile(filepath.Join(dir, ".dfm.lock"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer unlock()
	fmt.Println("ready")
	// Hold until the parent closes stdin or dies, so a crashed test run can't leak a holder.
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

func childOpen() int {
	if s := os.Getenv("DFM_STORE_TEST_LOCK_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		lockTimeout = d
	}
	cfg := &config.Config{State: config.StateConfig{URL: "file://" + os.Getenv("DFM_STORE_TEST_DB")}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, _, err := Open(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = db.Close()
	return 0
}

func childCmd(t *testing.T, mode, cacheDir string, extraEnv ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		childModeEnv+"="+mode,
		"TURSO_GO_CACHE_DIR="+cacheDir,
		"DFM_STORE_TEST_DB="+filepath.Join(t.TempDir(), "state.db"),
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	return cmd
}

func skipNonUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("native-library lock is unix-only")
	}
}

func TestOpen_ConcurrentColdCache_AllSucceed(t *testing.T) {
	skipNonUnix(t)
	cache := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]string, 8)
	for i := range errs {
		wg.Go(func() {
			if out, err := childCmd(t, "open", cache).CombinedOutput(); err != nil {
				errs[i] = fmt.Sprintf("child %d: %v: %s", i, err, out)
			}
		})
	}
	wg.Wait()
	for _, e := range errs {
		if e != "" {
			t.Error(strings.TrimSpace(e))
		}
	}
}

func TestOpen_CorruptCache_Repairs(t *testing.T) {
	skipNonUnix(t)
	cache := t.TempDir()
	if out, err := childCmd(t, "open", cache).CombinedOutput(); err != nil {
		t.Fatalf("warm: %v: %s", err, out)
	}
	libs, _ := filepath.Glob(filepath.Join(cache, "turso-go", "*", "*"))
	if len(libs) != 1 {
		t.Fatalf("want one extracted library, got %v", libs)
	}
	want := sha256File(t, libs[0])

	// Non-empty but short, as a crashed extractor leaves it; upstream re-extracts only empty files.
	if err := os.Truncate(libs[0], 1024); err != nil {
		t.Fatal(err)
	}
	if out, err := childCmd(t, "open", cache).CombinedOutput(); err != nil {
		t.Fatalf("open on corrupt cache: %v: %s", err, out)
	}
	if got := sha256File(t, libs[0]); got != want {
		t.Errorf("library hash after repair = %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(cache, "turso-go", ".dfm.lock")); err != nil {
		t.Errorf("lock file removed during repair: %v", err)
	}
}

func TestOpen_LockHolderKilled_NextOpenSucceeds(t *testing.T) {
	skipNonUnix(t)
	cache := t.TempDir()
	holder := startHolder(t, cache)
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()

	start := time.Now()
	out, err := childCmd(t, "open", cache, "DFM_STORE_TEST_LOCK_TIMEOUT=5s").CombinedOutput()
	if err != nil {
		t.Fatalf("open after holder SIGKILL: %v: %s", err, out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("open took %s after holder died", d)
	}
}

func TestOpen_HungLockHolder_TimesOut(t *testing.T) {
	skipNonUnix(t)
	cache := t.TempDir()
	startHolder(t, cache)

	out, err := childCmd(t, "open", cache, "DFM_STORE_TEST_LOCK_TIMEOUT=500ms").CombinedOutput()
	if err == nil {
		t.Fatalf("open succeeded while lock was held: %s", out)
	}
	lockPath := filepath.Join(cache, "turso-go", ".dfm.lock")
	if !strings.Contains(string(out), lockPath) {
		t.Errorf("error does not name lock path %s: %s", lockPath, out)
	}
}

func TestOpen_UnlockableCache_FallsBackToDriver(t *testing.T) {
	skipNonUnix(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cache := t.TempDir()
	if out, err := childCmd(t, "open", cache).CombinedOutput(); err != nil {
		t.Fatalf("warm: %v: %s", err, out)
	}
	dir := filepath.Join(cache, "turso-go")
	if err := os.Remove(filepath.Join(dir, ".dfm.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if out, err := childCmd(t, "open", cache).CombinedOutput(); err != nil {
		t.Fatalf("open with read-only cache dir: %v: %s", err, out)
	}
}

// startHolder runs a child that holds the cache lock until killed.
func startHolder(t *testing.T, cache string) *exec.Cmd {
	t.Helper()
	cmd := childCmd(t, "hold", cache)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("unexpected holder output %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("holder: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("holder never took the lock")
	}
	return cmd
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
