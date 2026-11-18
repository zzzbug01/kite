// Package sshcfg edits an OpenSSH client config without duplicating Host blocks.
package sshcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Host is one block appended to an ssh config.
type Host struct {
	Alias        string
	User         string
	IdentityFile string
	ProxyCommand string
	// ConfigFile is an explicit kite config. When set, ProxyCommand passes
	// --config so ssh can find a credential that is not in the default path.
	ConfigFile string
}

// Result describes an ssh config edit.
type Result struct {
	Path    string
	Changed bool
	Backup  string
}

// Merge appends a Host block when alias is not already a Host token.
// An existing block is left unchanged. The previous file is copied aside
// before the first modification.
func Merge(path string, host Host, now time.Time) (Result, error) {
	if err := validate(host); err != nil {
		return Result{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Result{}, err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	body, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}
	text := string(body)
	if HasHost(text, host.Alias) {
		return Result{Path: path, Changed: false}, nil
	}
	res := Result{Path: path, Changed: true}
	if existed && len(body) > 0 {
		backup := backupPath(path, now)
		if err := copyFile(backup, body); err != nil {
			return Result{}, err
		}
		res.Backup = backup
	}
	sep := ""
	if len(text) > 0 && !strings.HasSuffix(text, "\n") {
		sep = "\n"
	}
	if len(strings.TrimSpace(text)) > 0 {
		sep += "\n"
	}
	updated := text + sep + Block(host)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0o600); err != nil {
		return Result{}, err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return Result{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows cannot rename over an existing file.
		if err2 := os.Remove(path); err2 == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			os.Remove(tmp)
			return Result{}, err
		}
	}
	return res, nil
}

// HasHost reports whether content already has a Host block whose tokens
// include alias. Comments and patterns such as "Host *" do not count.
func HasHost(content, alias string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		fields := strings.Fields(trim)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Host") {
			continue
		}
		for _, tok := range fields[1:] {
			if tok == alias {
				return true
			}
		}
	}
	return false
}

// Block renders a Host stanza. Paths with spaces are quoted. Backslashes
// are written as forward slashes so OpenSSH does not treat them as escapes.
func Block(host Host) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host %s\n", host.Alias)
	fmt.Fprintf(&b, "  User %s\n", host.User)
	fmt.Fprintf(&b, "  IdentityFile %s\n", quote(slash(host.IdentityFile)))
	b.WriteString("  IdentitiesOnly yes\n")
	fmt.Fprintf(&b, "  ProxyCommand %s\n", proxyLine(host))
	return b.String()
}

func proxyLine(host Host) string {
	line := quote(slash(host.ProxyCommand))
	if host.ConfigFile != "" {
		line += " --config " + quote(slash(host.ConfigFile))
	}
	return line + " %n"
}

func validate(host Host) error {
	if host.Alias == "" || strings.ContainsAny(host.Alias, " \t\r\n") {
		return fmt.Errorf("ssh host alias %q is not a single token", host.Alias)
	}
	if host.User == "" || strings.ContainsAny(host.User, " \t\r\n") {
		return fmt.Errorf("ssh user %q is not a single token", host.User)
	}
	if host.IdentityFile == "" || strings.ContainsAny(host.IdentityFile, "\r\n\"") {
		return fmt.Errorf("identity file path is empty or contains a quote")
	}
	if host.ProxyCommand == "" || strings.ContainsAny(host.ProxyCommand, "\r\n\"") {
		return fmt.Errorf("proxy command path is empty or contains a quote")
	}
	if strings.ContainsAny(host.ConfigFile, "\r\n\"") {
		return fmt.Errorf("config path contains a quote or newline")
	}
	return nil
}

func slash(p string) string {
	// OpenSSH treats backslash as an escape. Write Windows paths with slashes
	// even when this code is tested on Linux, where filepath.ToSlash would
	// leave the backslashes in place.
	return strings.ReplaceAll(p, `\`, "/")
}

func quote(p string) string {
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

func backupPath(path string, now time.Time) string {
	return path + ".bak." + now.UTC().Format("20060102T150405Z")
}

func copyFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(body)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
