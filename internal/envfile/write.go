package envfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/fsx"
)

// SetKey writes key=value into the env file at path, replacing an
// existing "key=" line in place and appending one when absent. Every
// other line is preserved byte-for-byte, including whitespace the user
// added. The parent directory is created 0700 and the file is 0600 —
// this file holds credentials that must not live in config.toml.
func SetKey(path, key, value string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	var existing []string
	if data, err := os.ReadFile(path); err == nil {
		existing = strings.Split(string(data), "\n")
		// Split yields a trailing empty element when the file ends in
		// "\n"; dropping it stops blank lines accumulating per write.
		if n := len(existing); n > 0 && existing[n-1] == "" {
			existing = existing[:n-1]
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	line := key + "=" + value
	replaced := false
	for i, l := range existing {
		if !matchesKey(l, key) {
			continue
		}
		// Every occurrence is replaced, not just the first: Parse takes
		// the last one, so leaving a later duplicate would keep the old
		// value live and strand a superseded credential on disk.
		existing[i] = line
		replaced = true
	}
	if !replaced {
		existing = append(existing, line)
	}

	// AtomicWrite chmods the temp file before the rename, so the file is
	// 0600 from its first appearance on disk.
	return fsx.AtomicWrite(path, []byte(strings.Join(existing, "\n")+"\n"), 0o600)
}

// matchesKey reports whether a line assigns key, tolerating leading
// whitespace and the optional "export " prefix that Parse also strips.
func matchesKey(line, key string) bool {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "export ") || strings.HasPrefix(t, "export\t") {
		t = strings.TrimLeft(t[len("export"):], " \t")
	}
	return strings.HasPrefix(t, key+"=")
}
