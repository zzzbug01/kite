// Package auth compares the shared registration key without leaking it
// through an early length or content mismatch.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
)

// Equal reports whether got is the configured registration key.
// Both values are hashed before comparison so different lengths and
// differing prefixes take the same amount of time in the compare itself.
func Equal(got, want string) bool {
	sumGot := sha256.Sum256([]byte(got))
	sumWant := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(sumGot[:], sumWant[:]) == 1
}
