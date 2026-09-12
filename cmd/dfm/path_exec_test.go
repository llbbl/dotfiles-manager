package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pathInterpreter is one shell the generated block can be executed by.
// echoCmd has to differ because fish's $PATH is a list, not a
// colon-joined string. sourceCmd is "." everywhere except fish, which
// dropped it in 3.0; "source" is a bashism that dash does not have.
type pathInterpreter struct {
	name      string
	argv      []string // interpreter plus its no-rc flags; the script is appended
	echoCmd   string
	sourceCmd string
	split     func(string) []string
}

func splitColon(s string) []string { return strings.Split(s, ":") }
func splitFields(s string) []string {
	return strings.Fields(s)
}

// lookInterpreter resolves an interpreter or skips the test. Absolute
// paths are taken as-is so /bin/sh and /bin/bash can be pinned.
func lookInterpreter(t *testing.T, name string) string {
	t.Helper()
	if strings.HasPrefix(name, "/") {
		if _, err := os.Stat(name); err != nil {
			t.Skipf("%s not present: %v", name, err)
		}
		return name
	}
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not on PATH: %v", name, err)
	}
	return p
}

// runPathBlock sources block under interp with startPATH, then returns
// the resulting PATH filtered down to keep. Filtering keeps the
// assertions immune to whatever the interpreter's own startup files
// added.
func runPathBlock(t *testing.T, interp pathInterpreter, block, startPATH string, keep []string, times int) []string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "block")
	if err := os.WriteFile(file, []byte(block), 0o644); err != nil {
		t.Fatalf("write block: %v", err)
	}
	// "." is POSIX and works in bash and zsh too, so it is the safe
	// default when a caller does not name one.
	sourceCmd := interp.sourceCmd
	if sourceCmd == "" {
		sourceCmd = "."
	}
	var script strings.Builder
	for range times {
		script.WriteString(sourceCmd + " " + file + "\n")
	}
	script.WriteString(interp.echoCmd + "\n")

	argv := append(append([]string{}, interp.argv[1:]...), "-c", script.String())
	cmd := exec.Command(interp.argv[0], argv...)
	// A fixed env keeps a stray ENV/BASH_ENV from sourcing anything.
	cmd.Env = []string{"PATH=" + startPATH, "HOME=" + t.TempDir()}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	// dash reports an unknown command on stderr and still exits 0, so a
	// silent stderr is part of the contract, not just a debugging aid.
	if err != nil || stderr.Len() > 0 {
		t.Fatalf("%s run: %v\nstderr:\n%s\nscript:\n%s\nblock:\n%s",
			interp.name, err, stderr.String(), script.String(), block)
	}

	wanted := make(map[string]bool, len(keep))
	for _, k := range keep {
		wanted[k] = true
	}
	var got []string
	for _, p := range interp.split(strings.TrimSpace(string(out))) {
		if wanted[p] {
			got = append(got, p)
		}
	}
	return got
}

// pathDirs creates real directories for the symbolic names used by the
// behaviour tables. fish_add_path is the reason they have to exist on
// disk; the others do not care.
func pathDirs(t *testing.T, names ...string) map[string]string {
	t.Helper()
	root := t.TempDir()
	out := make(map[string]string, len(names))
	for _, n := range names {
		d := filepath.Join(root, n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		out[n] = d
	}
	return out
}

func mapDirs(m map[string]string, names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, m[n])
	}
	return out
}

// pathOrderCase is one row of a per-shell behaviour table: a starting
// PATH, a dir to add, and the resulting order once the block runs.
type pathOrderCase struct {
	name  string
	start []string
	add   string
	dir   string
	want  []string
}

func runPathOrderTable(t *testing.T, interp pathInterpreter, family string, cases []pathOrderCase) {
	t.Helper()
	all := []string{"a", "b", "c"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs := pathDirs(t, all...)
			add := dirs[tc.dir]
			block := renderPathBlock(family, pathMarkerID(tc.add, []string{add}), goldenTime, tc.add, []string{add})
			start := strings.Join(mapDirs(dirs, tc.start), ":")
			want := mapDirs(dirs, tc.want)

			got := runPathBlock(t, interp, block, start, mapDirs(dirs, all), 1)
			if !equalStrings(got, want) {
				t.Errorf("once: PATH = %v, want %v", got, want)
			}
			// Sourcing twice must not change position or count.
			got2 := runPathBlock(t, interp, block, start, mapDirs(dirs, all), 2)
			if !equalStrings(got2, want) {
				t.Errorf("twice: PATH = %v, want %v", got2, want)
			}
		})
	}
}

// promoteTable is the zsh behaviour: an existing entry is moved to the
// requested end rather than left where it is, and a duplicate sitting
// elsewhere on PATH collapses into that one entry.
var promoteTable = []pathOrderCase{
	{"promotes existing", []string{"a", "b", "c"}, pathDirectionPrepend, "b", []string{"b", "a", "c"}},
	{"adds missing", []string{"a", "c"}, pathDirectionPrepend, "b", []string{"b", "a", "c"}},
	{"collapses duplicates", []string{"a", "b", "c", "b"}, pathDirectionPrepend, "b", []string{"b", "a", "c"}},
	{"append moves last", []string{"a", "b", "c"}, pathDirectionAppend, "b", []string{"a", "c", "b"}},
	{"append adds missing", []string{"a", "c"}, pathDirectionAppend, "b", []string{"a", "c", "b"}},
}

// fishPromoteTable is promoteTable minus the collapsing. fish_add_path
// --move promotes the entry it manages but leaves a duplicate that was
// already elsewhere on PATH, measured against fish 3.7 on CI.
var fishPromoteTable = []pathOrderCase{
	{"promotes existing", []string{"a", "b", "c"}, pathDirectionPrepend, "b", []string{"b", "a", "c"}},
	{"adds missing", []string{"a", "c"}, pathDirectionPrepend, "b", []string{"b", "a", "c"}},
	{"keeps pre-existing duplicate", []string{"a", "b", "c", "b"}, pathDirectionPrepend, "b", []string{"b", "a", "c", "b"}},
	{"append moves last", []string{"a", "b", "c"}, pathDirectionAppend, "b", []string{"a", "c", "b"}},
	{"append adds missing", []string{"a", "c"}, pathDirectionAppend, "b", []string{"a", "c", "b"}},
}

// skipIfPresentTable is the current POSIX behaviour: an entry already
// on PATH is left alone. Promotion there is a separate change.
var skipIfPresentTable = []pathOrderCase{
	{"leaves existing", []string{"a", "b", "c"}, pathDirectionPrepend, "b", []string{"a", "b", "c"}},
	{"adds missing", []string{"a", "c"}, pathDirectionPrepend, "b", []string{"b", "a", "c"}},
	{"append adds missing", []string{"a", "c"}, pathDirectionAppend, "b", []string{"a", "c", "b"}},
	{"append leaves existing", []string{"a", "b", "c"}, pathDirectionAppend, "b", []string{"a", "b", "c"}},
}

func TestPathBlock_Zsh_PathOrder(t *testing.T) {
	zsh := lookInterpreter(t, "zsh")
	runPathOrderTable(t, pathInterpreter{
		name:      "zsh",
		argv:      []string{zsh, "-f"},
		echoCmd:   `echo "$PATH"`,
		sourceCmd: ".",
		split:     splitColon,
	}, "zsh", promoteTable)
}

func TestPathBlock_Fish_PathOrder(t *testing.T) {
	fish := lookInterpreter(t, "fish")
	runPathOrderTable(t, pathInterpreter{
		name:      "fish",
		argv:      []string{fish},
		echoCmd:   `echo $PATH`,
		sourceCmd: "source",
		split:     splitFields,
	}, "fish", fishPromoteTable)
}

// The POSIX body is executed under every POSIX shell available: the bash
// 3.2 behind /bin/sh and --shell profile on macOS, a modern bash, and
// dash, which is /bin/sh on Debian and Ubuntu.
func TestPathBlock_Posix_PathOrder(t *testing.T) {
	for _, sh := range []string{"bash", "/bin/sh", "/bin/bash", "dash"} {
		t.Run(strings.TrimPrefix(sh, "/bin/"), func(t *testing.T) {
			p := lookInterpreter(t, sh)
			runPathOrderTable(t, pathInterpreter{
				name:      sh,
				argv:      []string{p},
				echoCmd:   `echo "$PATH"`,
				sourceCmd: ".",
				split:     splitColon,
			}, "posix", skipIfPresentTable)
		})
	}
}

// The zsh block must not leave its loop variable behind in the shell it
// was sourced into.
func TestPathBlock_Zsh_UnsetsLoopVariable(t *testing.T) {
	zsh := lookInterpreter(t, "zsh")
	dirs := pathDirs(t, "a")
	block := renderPathBlock("zsh", "deadbeef", goldenTime, pathDirectionPrepend, []string{dirs["a"]})
	file := filepath.Join(t.TempDir(), "block")
	if err := os.WriteFile(file, []byte(block), 0o644); err != nil {
		t.Fatalf("write block: %v", err)
	}
	cmd := exec.Command(zsh, "-f", "-c", "source "+file+"; echo \"[${__dfm_d-unset}]\"")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("zsh run: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "[unset]" {
		t.Errorf("__dfm_d after sourcing = %s, want [unset]", got)
	}
}

// fish_add_path ignores a directory that is not on disk, so a managed
// block behaves differently under fish than under bash or zsh when an rc
// file reaches a machine before the directory exists. Pinned here so the
// divergence is caught if fish ever changes it.
func TestPathBlock_Fish_SkipsMissingDir(t *testing.T) {
	fish := lookInterpreter(t, "fish")
	missing := filepath.Join(t.TempDir(), "not-created")
	block := renderPathBlock("fish", pathMarkerID(pathDirectionPrepend, []string{missing}),
		goldenTime, pathDirectionPrepend, []string{missing})

	interp := pathInterpreter{
		name:      "fish",
		argv:      []string{fish},
		echoCmd:   `echo $PATH`,
		sourceCmd: "source",
		split:     splitFields,
	}
	got := runPathBlock(t, interp, block, "/usr/bin", []string{missing}, 1)
	if len(got) != 0 {
		t.Errorf("PATH picked up a missing dir: %v", got)
	}
}
