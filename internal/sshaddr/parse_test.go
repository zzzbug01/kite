package sshaddr

import (
	"os"
	"sort"
	"strings"
	"testing"
)

type mapFS map[string]string

func (m mapFS) ReadFile(name string) ([]byte, error) {
	b, ok := m[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(b), nil
}

func (m mapFS) Glob(pattern string) ([]string, error) {
	var out []string
	for name := range m {
		ok, err := pathMatch(pattern, name)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func TestAddressFromConfig(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"default port", "", "127.0.0.1:22"},
		{"port", "Port 2222\n", "127.0.0.1:2222"},
		{"first port", "Port 2200\nPort 2201\n", "127.0.0.1:2200"},
		{"loopback", "ListenAddress 127.0.0.1\nPort 22\n", "127.0.0.1:22"},
		{"loopback port", "ListenAddress 127.0.0.1:2222\n", "127.0.0.1:2222"},
		{"wildcard", "ListenAddress 0.0.0.0\nPort 2200\n", "127.0.0.1:2200"},
		{"v6 any", "ListenAddress [::]\nPort 22\n", "127.0.0.1:22"},
		{"pinned", "ListenAddress 192.0.2.10\nPort 22\n", "192.0.2.10:22"},
		{"match ignored", "Port 22\nMatch User root\n    Port 2222\n", "127.0.0.1:22"},
		{"comment", "# Port 9\nPort 2222 # extra\n", "127.0.0.1:2222"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := mapFS{"/etc/ssh/sshd_config": tc.body}
			p, err := Load(fs, "/etc/ssh/sshd_config")
			if err != nil && tc.body != "" {
				t.Fatal(err)
			}
			if got := Address(p, nil); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestIncludeAndSockets(t *testing.T) {
	fs := mapFS{
		"/etc/ssh/sshd_config":                 "Include /etc/ssh/sshd_config.d/*.conf\nPort 22\n",
		"/etc/ssh/sshd_config.d/10-port.conf":  "Port 2222\n",
		"/etc/ssh/sshd_config.d/50-later.conf": "Port 2200\n",
	}
	p, err := Load(fs, "/etc/ssh/sshd_config")
	if err != nil {
		t.Fatal(err)
	}
	// Include is first, so its Port is the first port OpenSSH would use.
	if got := Address(p, nil); got != "127.0.0.1:2222" {
		t.Fatalf("config %s ports %v", got, p.Ports)
	}
	if got := Address(p, []string{"0.0.0.0:22", "127.0.0.1:22"}); got != "127.0.0.1:22" {
		t.Fatalf("socket %s", got)
	}
	if got := Address(Parsed{}, []string{"192.0.2.10:2222"}); got != "192.0.2.10:2222" {
		t.Fatalf("non-loopback socket %s", got)
	}
	if got := Address(Parsed{}, []string{"[::1]:22"}); got != "[::1]:22" {
		t.Fatalf("v6 %s", got)
	}
}

func TestDecideDoesNotGuessSilently(t *testing.T) {
	out := Decide(Parsed{}, false, nil, []string{"127.0.0.1:2222", "127.0.0.1:80"}, func(addr string) bool {
		return addr == "127.0.0.1:2222"
	})
	if !out.Confident || out.Addr != "127.0.0.1:2222" {
		t.Fatalf("%+v", out)
	}
	guess := Decide(Parsed{}, false, nil, nil, func(string) bool { return false })
	if guess.Confident || !strings.Contains(guess.Detail, "not marking") {
		t.Fatalf("%+v", guess)
	}
	many := Decide(Parsed{}, false, nil, []string{"127.0.0.1:22", "127.0.0.1:2222"}, func(string) bool { return true })
	if many.Confident {
		t.Fatalf("%+v", many)
	}
}

func pathMatch(pattern, name string) (bool, error) {
	return match(pattern, name)
}
