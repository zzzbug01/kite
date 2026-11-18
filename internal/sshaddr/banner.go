package sshaddr

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// ProbeBanner reports whether addr answers with an SSH identification string.
func ProbeBanner(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if n == 0 || err != nil && n == 0 {
		return false
	}
	return strings.HasPrefix(string(buf[:n]), "SSH-")
}

// LoopbackListeners returns TCP listen addresses on loopback or wildcard.
// Failure returns nil so detection can continue from the config.
func LoopbackListeners() ([]string, error) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		return nil, nil
	}
	var out []string
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		out = append(out, ParseProcNet(string(b))...)
	}
	return out, nil
}

// ParseProcNet reads /proc/net/tcp or tcp6 and returns listening sockets
// bound to loopback or any address, as host:port.
func ParseProcNet(text string) []string {
	var out []string
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "0A" { // LISTEN
			continue
		}
		host, port, ok := parseProcAddr(fields[1])
		if !ok {
			continue
		}
		if host != "127.0.0.1" && host != "0.0.0.0" && host != "::" && host != "::1" {
			continue
		}
		if host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		out = append(out, net.JoinHostPort(host, strconv.Itoa(port)))
	}
	return out
}

func parseProcAddr(s string) (string, int, bool) {
	hostPort := strings.Split(s, ":")
	if len(hostPort) != 2 {
		return "", 0, false
	}
	port64, err := parseHex(hostPort[1])
	if err != nil {
		return "", 0, false
	}
	ipBytes, err := decodeHex(hostPort[0])
	if err != nil || (len(ipBytes) != 4 && len(ipBytes) != 16) {
		return "", 0, false
	}
	// /proc/net stores IPv4 little-endian.
	if len(ipBytes) == 4 {
		ipBytes[0], ipBytes[1], ipBytes[2], ipBytes[3] = ipBytes[3], ipBytes[2], ipBytes[1], ipBytes[0]
	}
	return net.IP(ipBytes).String(), int(port64), true
}

func parseHex(s string) (uint64, error) {
	var n uint64
	for _, c := range s {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n += uint64(c - '0')
		case c >= 'a' && c <= 'f':
			n += uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n += uint64(c-'A') + 10
		default:
			return 0, os.ErrInvalid
		}
	}
	return n, nil
}

func decodeHex(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, os.ErrInvalid
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		v, err := parseHex(s[i*2 : i*2+2])
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}
