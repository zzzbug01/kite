package sshaddr

import (
	"strconv"
	"strings"
)

// ParseSS reads `ss -H -ltnp` output and returns local addresses owned by sshd.
func ParseSS(out string) []string {
	var addrs []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "sshd") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if addr, ok := localAddr(field); ok {
				addrs = append(addrs, addr)
				break
			}
		}
	}
	return addrs
}

// ParseLsof reads `lsof -nP -iTCP -sTCP:LISTEN` output for sshd.
func ParseLsof(out string) []string {
	var addrs []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "sshd") || !strings.Contains(line, "LISTEN") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if addr, ok := localAddr(field); ok {
				addrs = appendUnique(addrs, addr)
			}
		}
	}
	return addrs
}

// ParseNetstat reads `netstat -ano -p tcp` and keeps LISTENING rows whose
// last field is an sshd pid.
func ParseNetstat(out string, pids map[string]bool) []string {
	var addrs []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		listening := false
		for _, f := range fields {
			if strings.EqualFold(f, "LISTENING") {
				listening = true
			}
		}
		if !listening {
			continue
		}
		pid := fields[len(fields)-1]
		if len(pids) > 0 && !pids[pid] {
			continue
		}
		// Local address is the field before the foreign address.
		for i, f := range fields {
			if strings.EqualFold(f, "LISTENING") && i >= 2 {
				if addr, ok := localAddr(fields[i-2]); ok {
					addrs = append(addrs, addr)
				}
				break
			}
		}
	}
	return addrs
}

func localAddr(field string) (string, bool) {
	field = strings.TrimSpace(field)
	if field == "" || strings.Contains(field, "*") && !strings.Contains(field, ":") {
		return "", false
	}
	host, port, err := splitLoose(field)
	if err != nil {
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	if host == "*" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return joinHostPort("127.0.0.1", n), true
	}
	return joinHostPort(strings.Trim(host, "[]"), n), true
}

func splitLoose(s string) (string, string, error) {
	if strings.HasPrefix(s, "[") {
		return split(s)
	}
	// IPv6 without brackets is ambiguous; ssh tools we parse use brackets or v4.
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return "", "", errSkip
	}
	return s[:i], s[i+1:], nil
}

var errSkip = errString("skip")

type errString string

func (e errString) Error() string { return string(e) }

func appendUnique(in []string, v string) []string {
	for _, s := range in {
		if s == v {
			return in
		}
	}
	return append(in, v)
}
