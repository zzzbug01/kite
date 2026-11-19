package agent

import (
	"errors"
	"os"
	"strings"

	"github.com/zzzbug01/kite/internal/protocol"
)

// SanitizeHostname turns a host name into an agent name: lower case, and
// only letters, digits, dots, underscores, and hyphens. Other characters
// become a single hyphen.
func SanitizeHostname(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	// Drop the domain. "macbook-pro.local" and "macbook-pro.example.com"
	// both become "macbook-pro".
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	var b strings.Builder
	dash := false
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			dash = false
		case r == '-':
			if b.Len() == 0 || dash {
				continue
			}
			b.WriteByte('-')
			dash = true
		default:
			if b.Len() == 0 || dash {
				continue
			}
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-.")
	if len(s) > 128 {
		s = strings.Trim(s[:128], "-.")
	}
	return s
}

// DefaultAgentName is the hostname, sanitized. "connect" is reserved.
func DefaultAgentName() (string, error) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("AGENT_NAME is required; the hostname could not be read")
	}
	name := SanitizeHostname(host)
	if name == "" || name == "connect" {
		return "", errors.New(`AGENT_NAME is required; set one explicitly ("connect" is reserved)`)
	}
	if err := protocol.CheckAgentName(name); err != nil {
		return "", err
	}
	return name, nil
}
