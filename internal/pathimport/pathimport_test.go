package pathimport

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llbbl/dotfiles-manager/internal/pathblock"
)

func TestScan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bare := func(line int, raw, dir string) Entry {
		return Entry{Line: line, EndLine: line, Raw: raw, Dir: dir, Direction: "prepend", Shape: ShapeBare}
	}
	guard := "case \":$PATH:\" in\n  *\":$HOME/.cargo/bin:\"*) ;;\n  *) export PATH=\"$HOME/.cargo/bin:$PATH\" ;;\nesac"
	guarded := Entry{Line: 1, EndLine: 4, Raw: guard, Dir: "$HOME/.cargo/bin", Direction: "prepend", Shape: ShapeGuarded}
	managed := pathblock.Render("posix", "12345678", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "append", []string{"/a", "/b", "/c"})
	tests := []struct {
		name string
		rc   string
		want Report
	}{
		{name: "empty"},
		{name: "irrelevant", rc: "#!/bin/sh\n# PATH is ignored\n\nexport EDITOR=vim\n"},
		{
			name: "bare quoted", rc: "# bashrc\nexport PATH=\"/opt/local/bin:$PATH\"\n",
			want: Report{Importable: []Entry{bare(2, `export PATH="/opt/local/bin:$PATH"`, "/opt/local/bin")}, Proposal: &Proposal{Direction: "prepend", Dirs: []string{"/opt/local/bin"}}},
		},
		{
			name: "home variants", rc: "export PATH=\"$HOME/go/bin:$PATH\"\nexport PATH=\"${HOME}/foo:$PATH\"\nexport PATH=\"~/cargo/bin:$PATH\"\nexport PATH=$HOME/extra:$PATH\n",
			want: Report{Importable: []Entry{
				bare(1, `export PATH="$HOME/go/bin:$PATH"`, "$HOME/go/bin"),
				bare(2, `export PATH="${HOME}/foo:$PATH"`, "${HOME}/foo"),
				bare(3, `export PATH="~/cargo/bin:$PATH"`, "~/cargo/bin"),
				bare(4, `export PATH=$HOME/extra:$PATH`, "$HOME/extra"),
			}, Proposal: &Proposal{Direction: "prepend", Dirs: []string{"$HOME/go/bin", "${HOME}/foo", "~/cargo/bin", "$HOME/extra"}}},
		},
		{
			name: "trailing comment", rc: "export PATH=\"/opt/local/bin:$PATH\" # note",
			want: Report{Importable: []Entry{bare(1, `export PATH="/opt/local/bin:$PATH" # note`, "/opt/local/bin")}, Proposal: &Proposal{Direction: "prepend", Dirs: []string{"/opt/local/bin"}}},
		},
		{name: "guarded", rc: guard, want: Report{Importable: []Entry{guarded}, Proposal: &Proposal{Direction: "prepend", Dirs: []string{"$HOME/.cargo/bin"}}}},
		{
			name: "guarded mismatch", rc: "case \":$PATH:\" in\n  *\":/opt/A:\"*) ;;\n  *) export PATH=\"/opt/B:$PATH\" ;;\nesac\n",
			want: Report{SkippedUnknown: []Entry{
				{Line: 1, Raw: `case ":$PATH:" in`, Reason: "guarded-block-mismatch"},
				{Line: 3, Raw: `  *) export PATH="/opt/B:$PATH" ;;`, Reason: "path-touches-unknown-shape"},
			}},
		},
		{
			name: "dynamic", rc: "eval \"$(mise activate zsh)\"\nsource ~/.nvm/nvm.sh\nsource ~/.oh-my-zsh.sh\n",
			want: Report{SkippedDynamic: []Entry{
				{Line: 1, Raw: `eval "$(mise activate zsh)"`, Reason: "eval-activate"},
				{Line: 2, Raw: `source ~/.nvm/nvm.sh`, Reason: "source"},
				{Line: 3, Raw: `source ~/.oh-my-zsh.sh`, Reason: "plugin-loader"},
			}},
		},
		{
			name: "guarded source", rc: "[[ -f ~/.nvm/nvm.sh ]] && source ~/.nvm/nvm.sh",
			want: Report{SkippedDynamic: []Entry{{Line: 1, Raw: "[[ -f ~/.nvm/nvm.sh ]] && source ~/.nvm/nvm.sh", Reason: "source"}}},
		},
		{
			name: "managed", rc: "# bashrc\n" + managed,
			want: Report{SkippedManaged: []Entry{{Line: 2, Raw: strings.Split(managed, "\n")[0], MarkerID: "12345678", Direction: "append", DirCount: 3}}},
		},
		{
			name: "unknown", rc: "PATH=$(some-script):$PATH\nexport PATH=\"${FOO_DIR}:$PATH\"\nif true; then PATH=/x; fi\nPATH=/unsupported:$PATH\n",
			want: Report{SkippedUnknown: []Entry{
				{Line: 1, Raw: "PATH=$(some-script):$PATH", Reason: "command-substitution"},
				{Line: 2, Raw: `export PATH="${FOO_DIR}:$PATH"`, Reason: "parameter-expansion"},
				{Line: 3, Raw: "if true; then PATH=/x; fi", Reason: "conditional"},
				{Line: 4, Raw: "PATH=/unsupported:$PATH", Reason: "path-touches-unknown-shape"},
			}},
		},
		{
			name: "duplicate bare", rc: "export PATH=\"$HOME/x:$PATH\"\nexport PATH=\"~/x:$PATH\"\n",
			want: Report{Importable: []Entry{
				bare(1, `export PATH="$HOME/x:$PATH"`, "$HOME/x"),
				{Line: 2, EndLine: 2, Raw: `export PATH="~/x:$PATH"`, Dir: "~/x", Direction: "prepend", Shape: ShapeBare, dupOf: 1},
			}, Proposal: &Proposal{Direction: "prepend", Dirs: []string{"$HOME/x"}}},
		},
		{
			name: "duplicate guarded", rc: guard + "\n" + guard,
			want: Report{Importable: []Entry{guarded, {Line: 5, EndLine: 8, Raw: guard, Dir: "$HOME/.cargo/bin", Direction: "prepend", Shape: ShapeGuarded, dupOf: 1}}, Proposal: &Proposal{Direction: "prepend", Dirs: []string{"$HOME/.cargo/bin"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want.File = "/fixture/bashrc"
			if got := Scan(tt.want.File, []byte(tt.rc)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Scan() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSplice(t *testing.T) {
	tests := []struct {
		name    string
		current string
		entries []Entry
		block   string
		want    string
	}{
		{name: "nothing importable", current: "# unchanged\n\n\n", block: "managed\n", want: "# unchanged\n\n\n"},
		{name: "first importable", current: "# before\nfirst\n# between\nsecond\n# after\n", entries: []Entry{{Line: 4, EndLine: 4}, {Line: 2, EndLine: 2}}, block: "managed\nbody\n", want: "# before\nmanaged\nbody\n# between\n# after\n"},
		{name: "guarded range", current: "# before\ncase\nguard\nexport\nesac\n# after\n", entries: []Entry{{Line: 2, EndLine: 5}}, block: "managed\n", want: "# before\nmanaged\n# after\n"},
		{name: "blank runs", current: "# before\n\n\nfirst\n\n\nsecond\n\n \n# after\n", entries: []Entry{{Line: 4, EndLine: 4}, {Line: 7, EndLine: 7}}, block: "managed\n", want: "# before\n\nmanaged\n\n \n# after\n"},
		{name: "without trailing newline", current: "# before\nfirst", entries: []Entry{{Line: 2, EndLine: 2}}, block: "managed\n", want: "# before\nmanaged"},
		{name: "with trailing newline", current: "first\n", entries: []Entry{{Line: 1, EndLine: 1}}, block: "managed\n", want: "managed\n"},
		{name: "missing end line", current: "first\n# after\n", entries: []Entry{{Line: 1}}, block: "managed\n", want: "managed\n# after\n"},
		{name: "past eof", current: "# before", entries: []Entry{{Line: 3, EndLine: 3}}, block: "managed\n", want: "# before\nmanaged"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(Splice([]byte(tt.current), tt.entries, tt.block)); got != tt.want {
				t.Errorf("Splice() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderHuman(t *testing.T) {
	tests := []struct {
		name   string
		report Report
		want   string
	}{
		{name: "empty sentinel", report: Report{File: "fixture"}, want: "scanning fixture...\n\nno importable PATH lines found.\n"},
		{
			name: "all buckets", report: Report{
				File: "fixture", Importable: []Entry{{Line: 2, Raw: `export PATH="/a:$PATH"`, Dir: "/a", Shape: ShapeBare}, {Line: 3, Dir: "/b", Shape: ShapeGuarded}},
				SkippedDynamic: []Entry{{Line: 7, Raw: `eval "$(mise activate zsh)"`, Reason: "eval-activate"}},
				SkippedManaged: []Entry{{Line: 8, MarkerID: "12345678", Direction: "append", DirCount: 3}},
				SkippedUnknown: []Entry{{Line: 9, Raw: "PATH=$(some-script):$PATH", Reason: "command-substitution"}},
				Proposal:       &Proposal{Direction: "prepend", Dirs: []string{"/a", "/b"}},
			},
			want: "scanning fixture...\n\n" +
				"  IMPORTABLE (static, literal dir tokens):\n" +
				"    line   2:  export PATH=\"/a:$PATH\"\n    line   3:  case-guard for /b\n\n" +
				"  SKIPPED (dynamic — runs code at startup):\n    line   7:  eval \"$(mise activate zsh)\"        [eval-activate]\n\n" +
				"  SKIPPED (already dfm-managed):\n    line   8:  # dfm:path:12345678 (append, 3 dirs)\n\n" +
				"  SKIPPED (unknown shape — left alone):\n    line   9:  PATH=$(some-script):$PATH           [command-substitution]\n\n" +
				"proposal: fold 2 dirs into one prepend entry:\n  /a, /b\n\n",
		},
		{
			name: "duplicate note", report: Report{File: "fixture", Importable: []Entry{{Line: 2, Raw: "duplicate", dupOf: 1}}},
			want: "scanning fixture...\n\n  IMPORTABLE (static, literal dir tokens):\n    line   2:  duplicate  [duplicate of line 1]\n\n",
		},
		{
			name: "skipped only", report: Report{File: "fixture", SkippedDynamic: []Entry{{Line: 1, Raw: "source file", Reason: "source"}}},
			want: "scanning fixture...\n\n  SKIPPED (dynamic — runs code at startup):\n    line   1:  source file        [source]\n\nno importable PATH lines found.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			RenderHuman(&b, tt.report)
			if got := b.String(); got != tt.want {
				t.Errorf("RenderHuman() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJSONShape(t *testing.T) {
	t.Run("nil buckets", func(t *testing.T) {
		report := JSONShape(Report{File: "fixture"})
		if report.Importable == nil || report.SkippedDynamic == nil || report.SkippedManaged == nil || report.SkippedUnknown == nil {
			t.Fatal("nil bucket was not converted to an empty slice")
		}
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(data), `{"file":"fixture","importable":[],"skipped_dynamic":[],"skipped_managed":[],"skipped_unknown":[]}`; got != want {
			t.Errorf("JSON = %s, want %s", got, want)
		}
	})
	t.Run("strips duplicates without mutation", func(t *testing.T) {
		entry := Entry{Line: 2, EndLine: 5, Raw: "original", Dir: "/a", Direction: "prepend", Shape: ShapeGuarded, dupOf: 1}
		proposal := &Proposal{Direction: "prepend", Dirs: []string{"/a"}}
		original := Report{File: "fixture", Importable: []Entry{entry}, SkippedDynamic: []Entry{entry}, SkippedManaged: []Entry{entry}, SkippedUnknown: []Entry{entry}, Proposal: proposal}
		got := JSONShape(original)
		entry.dupOf = 0
		want := Report{File: "fixture", Importable: []Entry{entry}, SkippedDynamic: []Entry{entry}, SkippedManaged: []Entry{entry}, SkippedUnknown: []Entry{entry}, Proposal: proposal}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("JSONShape() = %#v, want %#v", got, want)
		}
		for _, bucket := range [][]Entry{original.Importable, original.SkippedDynamic, original.SkippedManaged, original.SkippedUnknown} {
			if bucket[0].dupOf != 1 {
				t.Error("JSONShape mutated the original report")
			}
		}
		data, err := json.Marshal(got.Importable[0])
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"line":2,"raw":"original","dir":"/a","direction":"prepend","shape":"guarded"}`; string(data) != want {
			t.Errorf("entry JSON = %s, want %s", data, want)
		}
	})
}
