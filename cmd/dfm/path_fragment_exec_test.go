package main

import (
	"strings"
	"testing"
)

// fragSpec is one managed entry to seed into a synthetic rc file.
type fragSpec struct {
	direction string
	dirs      []string
}

// renderRCBlocks builds the rc-file content a fragment is derived from,
// so the tests exercise the real render → parse → re-render path rather
// than constructing PathManagedEntry values by hand.
func renderRCBlocks(family string, specs []fragSpec) string {
	var b strings.Builder
	for _, s := range specs {
		b.WriteString(renderPathBlock(family, pathMarkerID(s.direction, s.dirs),
			goldenTime, s.direction, s.dirs))
	}
	return b.String()
}

func fragmentFor(family string, specs []fragSpec) (rc, fragment string) {
	rc = renderRCBlocks(family, specs)
	return rc, renderPathFragment(family, findPathManagedEntries([]byte(rc)))
}

// runFragmentOrderTable replays a behaviour table through the fragment
// instead of the rc block. rcFamily and fragFamily differ for zsh: the
// rc file carries zsh's native body while the fragment carries the
// POSIX one, and both have to land on the same PATH.
func runFragmentOrderTable(t *testing.T, interp pathInterpreter, rcFamily, fragFamily string, cases []pathOrderCase) {
	t.Helper()
	all := []string{"a", "b", "c"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs := pathDirs(t, all...)
			add := dirs[tc.dir]
			rc := renderRCBlocks(rcFamily, []fragSpec{{tc.add, []string{add}}})
			fragment := renderPathFragment(fragFamily, findPathManagedEntries([]byte(rc)))
			start := strings.Join(mapDirs(dirs, tc.start), ":")
			keep := mapDirs(dirs, all)
			want := mapDirs(dirs, tc.want)

			got := runPathBlock(t, interp, fragment, start, keep, 1)
			if !equalStrings(got, want) {
				t.Errorf("fragment once: PATH = %v, want %v", got, want)
			}
			if gotRC := runPathBlock(t, interp, rc, start, keep, 1); !equalStrings(got, gotRC) {
				t.Errorf("fragment PATH = %v, rc blocks gave %v", got, gotRC)
			}
			if got2 := runPathBlock(t, interp, fragment, start, keep, 2); !equalStrings(got2, want) {
				t.Errorf("fragment twice: PATH = %v, want %v", got2, want)
			}
		})
	}
}

func TestPathFragment_Posix_MatchesRCBlocks(t *testing.T) {
	for _, sh := range posixInterpreters {
		t.Run(strings.TrimPrefix(sh, "/bin/"), func(t *testing.T) {
			p := lookInterpreter(t, sh)
			runFragmentOrderTable(t, pathInterpreter{
				name:      sh,
				argv:      []string{p},
				echoCmd:   `echo "$PATH"`,
				sourceCmd: ".",
				split:     splitColon,
			}, "posix", "posix", promoteTable)
		})
	}
}

// zsh sources env.sh, not a native zsh fragment, because one file has to
// serve sh, bash and zsh alike. This pins that the POSIX body reaches the
// same PATH under zsh as zsh's own `path=()` body does.
func TestPathFragment_Zsh_MatchesNativeRCBlocks(t *testing.T) {
	zsh := lookInterpreter(t, "zsh")
	runFragmentOrderTable(t, pathInterpreter{
		name:      "zsh",
		argv:      []string{zsh, "-f"},
		echoCmd:   `echo "$PATH"`,
		sourceCmd: ".",
		split:     splitColon,
	}, "zsh", "posix", promoteTable)
}

func TestPathFragment_Fish_MatchesRCBlocks(t *testing.T) {
	fish := lookInterpreter(t, "fish")
	runFragmentOrderTable(t, pathInterpreter{
		name:      "fish",
		argv:      []string{fish},
		echoCmd:   `echo $PATH`,
		sourceCmd: "source",
		split:     splitFields,
	}, "fish", "fish", fishPromoteTable)
}

// A dir named by both entries is the only shape whose final position
// depends on which block runs first, so it is what pins prepend-before-
// append. Disjoint entries commute and would pass either way.
func TestPathFragment_PrependRunsBeforeAppend(t *testing.T) {
	specs := func(dirs map[string]string) []fragSpec {
		return []fragSpec{
			{pathDirectionPrepend, []string{dirs["a"], dirs["b"]}},
			{pathDirectionAppend, []string{dirs["b"]}},
		}
	}
	run := func(t *testing.T, interp pathInterpreter) {
		t.Helper()
		all := []string{"a", "b", "c"}
		dirs := pathDirs(t, all...)
		rc, fragment := fragmentFor("posix", specs(dirs))
		start := dirs["c"]
		keep := mapDirs(dirs, all)
		want := mapDirs(dirs, []string{"a", "c", "b"})

		got := runPathBlock(t, interp, fragment, start, keep, 1)
		if !equalStrings(got, want) {
			t.Errorf("fragment: PATH = %v, want %v", got, want)
		}
		if gotRC := runPathBlock(t, interp, rc, start, keep, 1); !equalStrings(got, gotRC) {
			t.Errorf("fragment PATH = %v, rc blocks gave %v", got, gotRC)
		}
	}

	for _, sh := range posixInterpreters {
		t.Run(strings.TrimPrefix(sh, "/bin/"), func(t *testing.T) {
			p := lookInterpreter(t, sh)
			run(t, pathInterpreter{
				name:      sh,
				argv:      []string{p},
				echoCmd:   `echo "$PATH"`,
				sourceCmd: ".",
				split:     splitColon,
			})
		})
	}
	t.Run("zsh", func(t *testing.T) {
		zsh := lookInterpreter(t, "zsh")
		run(t, pathInterpreter{
			name:      "zsh",
			argv:      []string{zsh, "-f"},
			echoCmd:   `echo "$PATH"`,
			sourceCmd: ".",
			split:     splitColon,
		})
	})
}
