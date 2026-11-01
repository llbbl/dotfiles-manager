//go:build !unix

package store

// goreleaser ships darwin and linux only; this keeps other builds compiling
// without the lock.
func lockFile(string) (func(), error) { return func() {}, nil }
