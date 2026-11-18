// Package sshaddr finds the local address kite-agent should dial for sshd.
//
// It reads sshd_config (including Include) and, when sshd is already
// listening, prefers a loopback socket. The relay cannot change this
// address later; only the agent on the machine chooses it.
package sshaddr

import (
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Parsed is the global (non-Match) Port and ListenAddress settings, in the
// order OpenSSH reads them. The first value of each keyword is the one
// OpenSSH keeps when several are set, except that every ListenAddress is
// used. Multiple Port lines all listen.
type Parsed struct {
	Ports   []int
	Listens []Listen
}

// Listen is one ListenAddress. Port is 0 when the line did not include one;
// sshd then uses the Port directive.
type Listen struct {
	Host string
	Port int
}

// FS reads config files. Glob results must be sorted by the caller or by
// the implementation; Load sorts them.
type FS interface {
	ReadFile(name string) ([]byte, error)
	Glob(pattern string) ([]string, error)
}

// Load reads path and any Include directives that appear before a Match block.
func Load(fs FS, path string) (Parsed, error) {
	seen := map[string]struct{}{}
	return load(fs, path, seen, 0)
}

func load(fs FS, file string, seen map[string]struct{}, depth int) (Parsed, error) {
	if depth > 8 {
		return Parsed{}, fmt.Errorf("sshd_config include depth exceeded at %s", file)
	}
	if _, ok := seen[file]; ok {
		return Parsed{}, nil
	}
	seen[file] = struct{}{}
	body, err := fs.ReadFile(file)
	if err != nil {
		return Parsed{}, err
	}
	var out Parsed
	inMatch := false
	dir := filepath.Dir(file)
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		line = stripComment(line)
		fields := fields(line)
		if len(fields) == 0 {
			continue
		}
		key := strings.ToLower(fields[0])
		if key == "match" {
			inMatch = true
			continue
		}
		if inMatch {
			continue
		}
		switch key {
		case "include":
			for _, pat := range fields[1:] {
				pat = unquote(pat)
				if !filepath.IsAbs(pat) {
					pat = filepath.Join(dir, pat)
				}
				matches, err := fs.Glob(pat)
				if err != nil {
					return Parsed{}, err
				}
				sort.Strings(matches)
				if len(matches) == 0 && !strings.ContainsAny(pat, "*?[") {
					matches = []string{pat}
				}
				for _, m := range matches {
					inc, err := load(fs, m, seen, depth+1)
					if err != nil {
						return Parsed{}, err
					}
					out.Ports = append(out.Ports, inc.Ports...)
					out.Listens = append(out.Listens, inc.Listens...)
				}
			}
		case "port":
			if len(fields) < 2 {
				continue
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 1 || n > 65535 {
				continue
			}
			out.Ports = append(out.Ports, n)
		case "listenaddress":
			if len(fields) < 2 {
				continue
			}
			ln, err := parseListen(unquote(fields[1]))
			if err != nil {
				continue
			}
			out.Listens = append(out.Listens, ln)
		}
	}
	return out, nil
}

func stripComment(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	// A comment can follow the tokens. Quoted values are not expected to
	// contain '#'.
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line
}

func fields(line string) []string {
	return strings.Fields(line)
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func parseListen(s string) (Listen, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Listen{}, fmt.Errorf("empty")
	}
	if strings.HasPrefix(s, "[") {
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			host = strings.Trim(s, "[]")
			return Listen{Host: host}, nil
		}
		n, err := strconv.Atoi(port)
		if err != nil {
			return Listen{}, err
		}
		return Listen{Host: host, Port: n}, nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return Listen{Host: s}, nil
	}
	// "127.0.0.1:22" splits. A bare IPv6 without brackets does not arrive here
	// from sshd_config; sshd requires brackets.
	n, err := strconv.Atoi(port)
	if err != nil {
		return Listen{Host: s}, nil
	}
	return Listen{Host: host, Port: n}, nil
}

// Address picks the dial address. sockets are "host:port" listeners belonging
// to sshd. A loopback socket wins. With no socket, ListenAddress is honored
// when it pins loopback or a specific host; a wildcard or an absent address
// becomes 127.0.0.1. The port is the first Port, or the port on that
// ListenAddress, or 22.
func Address(p Parsed, sockets []string) string {
	if addr := preferSocket(sockets); addr != "" {
		return addr
	}
	port := 22
	if len(p.Ports) > 0 {
		port = p.Ports[0]
	}
	host := "127.0.0.1"
	for _, ln := range p.Listens {
		if isWildcard(ln.Host) {
			host = "127.0.0.1"
			if ln.Port > 0 {
				port = ln.Port
			}
			break
		}
		if isLoopback(ln.Host) {
			host = "127.0.0.1"
			if ln.Host == "::1" && !hasV4Loopback(p.Listens) {
				host = "::1"
			}
			if ln.Port > 0 {
				port = ln.Port
			}
			break
		}
	}
	if len(p.Listens) > 0 && !hasWildcard(p.Listens) && !hasLoopback(p.Listens) {
		host = p.Listens[0].Host
		if p.Listens[0].Port > 0 {
			port = p.Listens[0].Port
		}
	} else if len(p.Listens) > 0 {
		// The loop above breaks on the first wildcard or loopback. If that
		// listen carried a port, it is already applied.
	}
	return joinHostPort(host, port)
}

func preferSocket(sockets []string) string {
	var loop, other string
	for _, s := range sockets {
		host, port, err := split(s)
		if err != nil || port == "" {
			continue
		}
		addr := joinHostPort(normalizeHost(host), atoiPort(port))
		if isLoopback(host) && loop == "" {
			loop = addr
			continue
		}
		if other == "" && !isWildcard(host) {
			other = addr
		}
		if other == "" && isWildcard(host) {
			other = joinHostPort("127.0.0.1", atoiPort(port))
		}
	}
	if loop != "" {
		return loop
	}
	return other
}

func split(s string) (string, string, error) {
	if strings.HasPrefix(s, "[") {
		return net.SplitHostPort(s)
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", "", err
	}
	return host, port, nil
}

func atoiPort(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func joinHostPort(host string, port int) string {
	host = strings.Trim(host, "[]")
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func normalizeHost(host string) string {
	if isLoopback(host) && host != "::1" {
		return "127.0.0.1"
	}
	if isWildcard(host) {
		return "127.0.0.1"
	}
	return host
}

func isWildcard(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "", "*", "0.0.0.0", "::", "::0":
		return true
	default:
		return false
	}
}

func isLoopback(host string) bool {
	h := strings.Trim(host, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func hasV4Loopback(ls []Listen) bool {
	for _, ln := range ls {
		if ln.Host == "127.0.0.1" || ln.Host == "localhost" {
			return true
		}
	}
	return false
}

func hasWildcard(ls []Listen) bool {
	for _, ln := range ls {
		if isWildcard(ln.Host) {
			return true
		}
	}
	return false
}

func hasLoopback(ls []Listen) bool {
	for _, ln := range ls {
		if isLoopback(ln.Host) {
			return true
		}
	}
	return false
}
