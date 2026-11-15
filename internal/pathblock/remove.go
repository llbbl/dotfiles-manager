package pathblock

// RemoveBlocks splices blocks last-first to preserve offsets and removes their separator blank lines.
func RemoveBlocks(content []byte, entries []Entry) []byte {
	out := content
	for i := len(entries) - 1; i >= 0; i-- {
		start, end := entries[i].BlockStart, entries[i].BlockEnd
		// The separator is "\n" on its own, or "\r\n" on a CRLF file.
		if start >= 2 && out[start-1] == '\n' {
			switch {
			case out[start-2] == '\n':
				start--
			case out[start-2] == '\r' && start >= 3 && out[start-3] == '\n':
				start -= 2
			}
		}
		out = append(out[:start:start], out[end:]...)
	}
	return out
}
