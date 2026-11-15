package alias

import (
	"slices"
	"testing"
)

func TestLine_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		family  string
		alias   string
		command string
		want    string
	}{
		{
			name:    "posix simple",
			family:  "posix",
			alias:   "ll",
			command: "ls -la",
			want:    "alias ll='ls -la'\n",
		},
		{
			// POSIX shells can't escape ' inside '...'; the idiom is to
			// close the quote, emit \', and reopen: '\''.
			name:    "posix single quote",
			family:  "posix",
			alias:   "g",
			command: "echo 'hi'",
			want:    `alias g='echo '\''hi'\'''` + "\n",
		},
		{
			// Inside single quotes $ has no special meaning, so no escaping.
			name:    "posix dollar literal",
			family:  "posix",
			alias:   "p",
			command: "echo $PATH",
			want:    "alias p='echo $PATH'\n",
		},
		{
			// Inside POSIX single quotes a backslash is literal too.
			name:    "posix backslash",
			family:  "posix",
			alias:   "b",
			command: `echo a\b`,
			want:    "alias b='echo a\\b'\n",
		},
		{
			name:    "fish simple",
			family:  "fish",
			alias:   "ll",
			command: "ls -la",
			want:    "alias ll 'ls -la'\n",
		},
		{
			// Fish single-quote strings: only \' and \\ are special.
			name:    "fish single quote",
			family:  "fish",
			alias:   "g",
			command: "echo 'hi'",
			want:    `alias g 'echo \'hi\''` + "\n",
		},
		{
			// Backslash gets doubled in fish so the runtime sees one \.
			name:    "fish backslash",
			family:  "fish",
			alias:   "b",
			command: `echo a\b`,
			want:    `alias b 'echo a\\b'` + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Line(tc.family, tc.alias, tc.command)
			if got != tc.want {
				t.Errorf("Line(%q,%q,%q)\n got: %q\nwant: %q",
					tc.family, tc.alias, tc.command, got, tc.want)
			}
		})
	}
}

const (
	defOpen  = "# >>> dfm:aliases >>>\n"
	defClose = "# <<< dfm:aliases <<<\n"
	gitOpen  = "# >>> dfm:group git >>>\n"
	gitClose = "# <<< dfm:group git <<<\n"
)

func TestUpsert_TableDriven(t *testing.T) {
	ll := "alias ll='ls'\n"
	gs := "alias gs='git status'\n"
	cases := []struct {
		name       string
		content    string
		group      string
		want       string
		wantAction string
	}{
		{"create default block", "# rc\n", "", "# rc\n" + defOpen + ll + defClose, "created"},
		{"create default block adds missing newline", "# rc", "", "# rc\n" + defOpen + ll + defClose, "created"},
		{"append into default block", "# rc\n" + defOpen + gs + defClose + "tail\n", "", "# rc\n" + defOpen + gs + ll + defClose + "tail\n", "appended"},
		{"create group block", "", "git", gitOpen + ll + gitClose, "created"},
		{"append into group block leaves default alone", defOpen + gs + defClose + gitOpen + gs + gitClose, "git", defOpen + gs + defClose + gitOpen + gs + ll + gitClose, "appended"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, action := Upsert(tc.content, tc.group, ll)
			if got != tc.want || action != tc.wantAction {
				t.Errorf("Upsert = (%q, %q), want (%q, %q)", got, action, tc.want, tc.wantAction)
			}
		})
	}
}

func TestRemoveEverywhere_TableDriven(t *testing.T) {
	ll := "alias ll='ls'\n"
	gs := "alias gs='git status'\n"
	cases := []struct {
		name    string
		content string
		want    string
		counts  RemovalCounts
	}{
		{"legacy block", "# top\n# >>> dfm:alias ll >>>\n" + ll + "# <<< dfm:alias ll <<<\nkeep\n", "# top\nkeep\n", RemovalCounts{LegacyBlocks: 1}},
		{"shared block keeps other aliases", defOpen + ll + gs + defClose, defOpen + gs + defClose, RemovalCounts{SharedEvicted: 1}},
		{"emptied shared block dropped", "# top\n" + defOpen + ll + defClose + "keep\n", "# top\nkeep\n", RemovalCounts{SharedEvicted: 1, EmptiedBlocks: 1}},
		{"group block keeps other aliases", gitOpen + gs + ll + gitClose, gitOpen + gs + gitClose, RemovalCounts{SharedEvicted: 1}},
		{"emptied group block dropped", gitOpen + ll + gitClose + "keep\n", "keep\n", RemovalCounts{SharedEvicted: 1, EmptiedBlocks: 1}},
		{"bare line", "# top\n" + ll + "keep\n", "# top\nkeep\n", RemovalCounts{BareLines: 1}},
		{"absent", "# top\n" + gs, "# top\n" + gs, RemovalCounts{}},
		{"prefix collision lla survives", "alias lla='x'\n" + defOpen + "alias lla='x'\n" + defClose, "alias lla='x'\n" + defOpen + "alias lla='x'\n" + defClose, RemovalCounts{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, counts := RemoveEverywhere("posix", "ll", tc.content)
			if got != tc.want || counts != tc.counts {
				t.Errorf("RemoveEverywhere = (%q, %+v), want (%q, %+v)", got, counts, tc.want, tc.counts)
			}
		})
	}
}

func TestCount_TableDriven(t *testing.T) {
	ll := "alias ll='ls'\n"
	legacy := "# >>> dfm:alias ll >>>\n" + ll + "# <<< dfm:alias ll <<<\n"
	cases := []struct {
		name    string
		family  string
		content string
		want    int
	}{
		{"absent", "posix", "alias gs='git status'\n", 0},
		{"prefix collision lla is not ll", "posix", "alias lla='x'\n" + defOpen + "alias lla='x'\n" + defClose, 0},
		{"bare", "posix", ll, 1},
		{"legacy block", "posix", legacy, 1},
		{"default block", "posix", defOpen + ll + defClose, 1},
		{"group block", "posix", gitOpen + ll + gitClose, 1},
		{"every kind", "posix", legacy + defOpen + ll + defClose + gitOpen + ll + gitClose + ll, 4},
		{"fish ignores posix syntax", "fish", "alias ll 'ls'\n" + defOpen + "alias ll 'la'\n" + defClose + ll, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Count(tc.family, "ll", tc.content); got != tc.want {
				t.Errorf("Count = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParse_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		family  string
		content string
		want    []Entry
	}{
		{"posix", "posix", "alias ll='ls -la'\n  alias g=\"git\"\n", []Entry{{"ll", "ls -la"}, {"g", "git"}}},
		{"fish", "fish", "alias ll 'ls -la'\nalias g='git'\n", []Entry{{"ll", "ls -la"}}},
		{"noise", "posix", "# comment\nexport X=1\nalias bad-name='x'\nalias e=''\n\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.family, tc.content); !slices.Equal(got, tc.want) {
				t.Errorf("Parse = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestValidGroup_RejectsRegexAndSpaceChars(t *testing.T) {
	for _, tc := range []struct {
		group string
		want  bool
	}{
		{"a-b_1", true},
		{"a.b", false},
		{"a b", false},
	} {
		if got := ValidGroup.MatchString(tc.group); got != tc.want {
			t.Errorf("ValidGroup.MatchString(%q) = %v, want %v", tc.group, got, tc.want)
		}
	}
}
