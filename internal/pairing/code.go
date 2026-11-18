// Package pairing is the short-lived pairing-code state machine and the
// file-backed per-client token store.
//
// A pairing code is an 8-character session token from an unambiguous
// alphabet, shown as ABCD-EFGH. It is not the registration key. The relay
// stores only a hash of the code. Codes expire and are single-use.
//
// Client tokens issued when a code is redeemed are also stored as hashes,
// under STATE_DIR (default /var/lib/kite). The raw token is returned once.
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
	"time"
)

// Alphabet excludes 0, O, 1, and I so a code can be read aloud.
const Alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const (
	// DefaultTTL is how long a new pairing code stays valid.
	DefaultTTL = 10 * time.Minute
	// MaxTTL is the longest code lifetime the relay will store.
	MaxTTL  = 30 * time.Minute
	codeLen = 8
)

var (
	// ErrRateLimited is returned when an IP or agent has asked too often.
	ErrRateLimited = errors.New("rate limited")
	// ErrCodeInvalid means the code is unknown. The error text does not
	// include the code.
	ErrCodeInvalid = errors.New("pairing code is not valid")
	// ErrCodeExpired means the code was real and its lifetime ended.
	ErrCodeExpired = errors.New("pairing code expired")
	// ErrCodeUsed means the code was already redeemed.
	ErrCodeUsed = errors.New("pairing code already used")
	// ErrTooManyCodes means this agent already has the maximum number of
	// outstanding codes.
	ErrTooManyCodes = errors.New("too many outstanding pairing codes")
)

// NormalizeCode accepts "ABCD-EFGH", "abcdefgh", and the same with spaces.
// It returns the 8-character uppercase form with no hyphen.
func NormalizeCode(in string) (string, error) {
	in = strings.ToUpper(strings.TrimSpace(in))
	in = strings.ReplaceAll(in, "-", "")
	in = strings.ReplaceAll(in, " ", "")
	if len(in) != codeLen {
		return "", ErrCodeInvalid
	}
	for _, r := range in {
		if !strings.ContainsRune(Alphabet, r) {
			return "", ErrCodeInvalid
		}
	}
	return in, nil
}

// FormatCode renders an 8-character normalized code as ABCD-EFGH.
func FormatCode(norm string) string {
	if len(norm) != codeLen {
		return norm
	}
	return norm[:4] + "-" + norm[4:]
}

func newCode() (string, error) {
	var b [codeLen]byte
	n := len(Alphabet)
	// 256 is divisible by 32, so a single byte modulo n is uniform.
	// Rejection sampling still guards a future alphabet whose size does not
	// divide 256.
	limit := 256 - (256 % n)
	var buf [1]byte
	for i := 0; i < codeLen; i++ {
		for {
			if _, err := rand.Read(buf[:]); err != nil {
				return "", err
			}
			if int(buf[0]) < limit {
				b[i] = Alphabet[int(buf[0])%n]
				break
			}
		}
	}
	return string(b[:]), nil
}

func hashCode(norm string) []byte {
	sum := sha256.Sum256([]byte("kite-pair-code-v1:" + norm))
	return sum[:]
}

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte("kite-client-token-v1:" + raw))
	return sum[:]
}

// matchHash returns the index of sum in hashes, or -1. It always compares
// against every entry and one dummy, so a hit on the first code is not a
// shorter compare than a miss.
func matchHash(hashes [][]byte, sum []byte) int {
	found := -1
	var dummy [32]byte
	for i, h := range hashes {
		if len(h) == len(sum) && subtle.ConstantTimeCompare(h, sum) == 1 {
			found = i
		}
	}
	_ = subtle.ConstantTimeCompare(sum, dummy[:])
	return found
}

// SanitizeLabel keeps a client label safe to embed in an authorized_keys
// comment and in log lines. Empty input becomes "client".
func SanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "client"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// PairComment is the authorized_keys comment for a key kite installed.
func PairComment(label string, now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return "kite-pair " + SanitizeLabel(label) + " " + now.UTC().Format("2006-01-02")
}
