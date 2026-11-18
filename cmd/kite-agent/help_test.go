package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestSubcommandHelpMentionsItself(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("KITE_CONFIG", "")
	t.Setenv("USERPROFILE", home)
	t.Setenv("RELAY_ADDR", "")
	t.Setenv("REGISTRATION_KEY", "")
	t.Setenv("AGENT_NAME", "")
	t.Setenv("LOCAL_SSH_ADDR", "")
	t.Setenv("LOCAL_SSH_ADDR_AUTO", "")
	commands := []string{
		"install", "uninstall", "status", "start", "stop", "restart",
		"list", "upgrade", "setup", "pair", "unpair",
	}
	seen := map[string]string{}
	for _, cmd := range commands {
		text := captureStderr(t, func() error {
			return run([]string{cmd, "--help"})
		})
		if !mentionsCommand(text, cmd) {
			t.Fatalf("%s help does not mention %q outside the usage line:\n%s", cmd, cmd, text)
		}
		seen[cmd] = text
	}
	if strings.Contains(helpProse(seen["uninstall"]), "local ssh health") {
		t.Fatalf("uninstall help is the status text:\n%s", seen["uninstall"])
	}
	if strings.Contains(helpProse(seen["start"]), "local ssh health") || strings.Contains(helpProse(seen["stop"]), "local ssh health") {
		t.Fatal("start/stop help is the status text")
	}
}

func helpProse(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "-") || strings.HasPrefix(trim, "Usage:") {
			continue
		}
		b.WriteString(strings.ToLower(line))
		b.WriteByte('\n')
	}
	return b.String()
}

func mentionsCommand(text, cmd string) bool {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Usage:") {
			continue
		}
		b.WriteString(strings.ToLower(line))
		b.WriteByte('\n')
	}
	return containsWord(b.String(), cmd)
}

func containsWord(s, word string) bool {
	for _, field := range strings.FieldsFunc(s, func(r rune) bool {
		return r < 'a' || r > 'z'
	}) {
		if field == word {
			return true
		}
	}
	return false
}

func captureStderr(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	runErr := fn()
	w.Close()
	os.Stderr = old
	body, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	return string(body)
}
