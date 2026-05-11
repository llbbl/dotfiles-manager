// Package ids generates sortable, lowercase base32 identifiers that
// combine a nanosecond timestamp with random bytes.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"time"
)

var encoding = base32.NewEncoding("0123456789abcdefghijklmnopqrstuv").WithPadding(base32.NoPadding)

// New returns a 26-character lowercase base32 ID built from a
// nanosecond timestamp plus 10 random bytes.
func New() (string, error) {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(time.Now().UnixNano()))
	if _, err := rand.Read(buf[8:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return encoding.EncodeToString(buf[:]), nil
}
