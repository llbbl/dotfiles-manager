package config

import (
	"strings"
	"testing"
)

func TestRemoteURLDisplay_Hostless(t *testing.T) {
	for _, scheme := range []string{"libsql", "https", "http", "wss", "ws"} {
		for _, shape := range []string{"single-slash", "opaque"} {
			t.Run(scheme+"/"+shape, func(t *testing.T) {
				raw := scheme + ":/h/%zz?authToken=SECRET-88"
				if shape == "opaque" {
					raw = scheme + ":h/path?authToken=SECRET-88"
				}
				if ScrubURL(raw) != "<redacted remote URL>" {
					t.Error("hostless remote URL was not redacted")
				}
				cfg := &Config{State: StateConfig{URL: raw}}
				shown := cfg.Redacted()
				if shown.State.URL != "<redacted remote URL>" {
					t.Error("redacted config retained the hostless remote URL")
				}
				data, err := shown.EncodeTOML()
				if err != nil {
					t.Fatal("encoding redacted config failed")
				}
				if strings.Contains(string(data), "SECRET-88") {
					t.Error("config display contains the embedded credential")
				}
			})
		}
	}
}

func TestSplitAuthToken_Malformed(t *testing.T) {
	clean, token, err := SplitAuthToken("libsql://h/%zz?authToken=SECRET-88")
	if clean != "" || token != "" || err == nil || err.Error() != "invalid Turso URL" {
		t.Error("malformed URL did not fail closed")
	}
}

func TestScrubURL_MalformedRemote(t *testing.T) {
	for _, raw := range []string{
		"libsql://h/%zz?authToken=SECRET-88",
		"https://h/%zz?auth_token=SECRET-88",
		"libsql://[bad?token=SECRET-88",
	} {
		if ScrubURL(raw) != "<redacted remote URL>" {
			t.Error("malformed remote URL was not redacted")
		}
	}
}

func TestSplitAuthToken(t *testing.T) {
	tests := []struct {
		name, raw, clean, token string
	}{
		{"camel case", "libsql://h?authToken=SECRET-88", "libsql://h", "SECRET-88"},
		{"snake case", "libsql://h?auth_token=SECRET-88", "libsql://h", "SECRET-88"},
		{"short key", "libsql://h?token=SECRET-88", "libsql://h", "SECRET-88"},
		{"other params", "libsql://h/db?authToken=SECRET-88&mode=read#part", "libsql://h/db?mode=read#part", "SECRET-88"},
		{"no token", "libsql://h?mode=read", "libsql://h?mode=read", ""},
		{"no query", "libsql://h", "libsql://h", ""},
		{"empty query", "libsql://h?", "libsql://h", ""},
		{"empty token", "libsql://h?authToken=", "libsql://h", ""},
		{"key order", "libsql://h?token=last&auth_token=middle&authToken=first", "libsql://h", "first"},
		{"empty first key", "libsql://h?authToken=&auth_token=middle&token=last", "libsql://h", "middle"},
		{"empty first two keys", "libsql://h?authToken=&auth_token=&token=last", "libsql://h", "last"},
		{"repeated key", "libsql://h?authToken=&authToken=first&authToken=last", "libsql://h", "first"},
		{"encoded token", "libsql://h?authToken=a%2Bb%3D&mode=read", "libsql://h?mode=read", "a+b="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, token, err := SplitAuthToken(tt.raw)
			if err != nil {
				t.Fatal("valid URL rejected")
			}
			if clean != tt.clean {
				t.Error("clean URL differs from expected URL")
			}
			if token != tt.token {
				t.Error("extracted token differs from expected token")
			}
		})
	}
}
