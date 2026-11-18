// Package authkeys chooses and edits the authorized_keys file kite-agent
// writes during pairing.
package authkeys

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/zzzbug01/kite/internal/pairing"
)

// Kind selects the permission fix applied after a write.
type Kind int

const (
	// KindPOSIX is ~/.ssh/authorized_keys on Linux and macOS.
	KindPOSIX Kind = iota
	// KindWindowsUser is a normal user's authorized_keys.
	KindWindowsUser
	// KindWindowsAdmin is the Windows OpenSSH administrators file.
	// OpenSSH ignores ~/.ssh/authorized_keys for administrators when the
	// default sshd_config Match Group is in place.
	KindWindowsAdmin
)

// Path picks the authorized_keys file for this user.
// programData is used on Windows; pass "" to use C:\ProgramData.
// admin is ignored off Windows.
func Path(goos, home string, admin bool, programData string) (string, Kind) {
	// Windows paths are built with backslashes even when this function runs
	// in tests on Linux. filepath.Join would use the host separator.
	if goos == "windows" && admin {
		if strings.TrimSpace(programData) == "" {
			programData = `C:\ProgramData`
		}
		programData = strings.TrimRight(programData, `\/`)
		return programData + `\ssh\administrators_authorized_keys`, KindWindowsAdmin
	}
	if goos == "windows" {
		home = strings.TrimRight(home, `\/`)
		return home + `\.ssh\authorized_keys`, KindWindowsUser
	}
	return filepath.Join(home, ".ssh", "authorized_keys"), KindPOSIX
}

// Record is one key kite-agent installed.
type Record struct {
	Label       string
	Date        string
	Type        string
	Fingerprint string
	Line        string
}

// ValidateEd25519 accepts a single ssh-ed25519 public key. Comments and
// leading options are stripped. Newlines are rejected so a client cannot
// append a second key.
func ValidateEd25519(line string) (string, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", errors.New("public key must be one ssh-ed25519 line")
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", fmt.Errorf("public key: %w", err)
	}
	if pub.Type() != ssh.KeyAlgoED25519 {
		return "", errors.New("only ssh-ed25519 keys can be paired")
	}
	out := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if strings.ContainsAny(out, "\r\n") {
		return "", errors.New("normalized public key was not one line")
	}
	return out, nil
}

// Fingerprint returns the OpenSSH SHA256 fingerprint of an authorized key line.
func Fingerprint(line string) (string, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(pub), nil
}

// Upsert appends keyLine with comment when that key blob is not already
// present. An existing kite-pair line for the same blob gets the new comment
// so a later revoke by label still finds it. The write refuses to follow a
// symlink at path.
func Upsert(path string, keyLine, comment string, kind Kind, owner string) error {
	keyLine, err := ValidateEd25519(keyLine)
	if err != nil {
		return err
	}
	comment = strings.TrimSpace(comment)
	if comment == "" || strings.ContainsAny(comment, "\r\n") {
		return errors.New("key comment must be one line")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil && kind == KindPOSIX {
		return err
	}
	existing, err := readKeys(path)
	if err != nil {
		return err
	}
	blob := keyBlob(keyLine)
	var b strings.Builder
	found := false
	for _, line := range existing {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if blob != "" && keyBlob(line) == blob {
			found = true
			if isPairComment(commentOf(line)) {
				fmt.Fprintf(&b, "%s %s\n", keyLine, comment)
				continue
			}
		}
		b.WriteString(strings.TrimRight(line, "\r"))
		b.WriteByte('\n')
	}
	if !found {
		fmt.Fprintf(&b, "%s %s\n", keyLine, comment)
	}
	return writeKeys(path, []byte(b.String()), kind, owner)
}

// List returns kite-pair keys in path. A missing file is an empty list.
func List(path string) ([]Record, error) {
	lines, err := readKeys(path)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, line := range lines {
		rec, ok := parsePair(line)
		if ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// Revoke removes kite-pair lines whose label matches. Other lines are kept.
// The number of removed lines is returned. A missing file removes nothing.
func Revoke(path, label string, kind Kind, owner string) (int, error) {
	label = pairing.SanitizeLabel(label)
	lines, err := readKeys(path)
	if err != nil {
		return 0, err
	}
	if len(lines) == 0 {
		return 0, nil
	}
	var b strings.Builder
	removed := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		if rec, ok := parsePair(line); ok && rec.Label == label {
			removed++
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if removed == 0 {
		return 0, nil
	}
	// An empty authorized_keys file stays in place. Deleting it would drop
	// the ACL and, on Windows, let a later create inherit a looser DACL.
	body := []byte{}
	if b.Len() > 0 {
		body = []byte(b.String())
	}
	if err := writeKeys(path, body, kind, owner); err != nil {
		return 0, err
	}
	return removed, nil
}

func readKeys(path string) ([]string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink; refusing to follow it", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	return strings.Split(strings.TrimRight(text, "\n"), "\n"), nil
}

func writeKeys(path string, body []byte, kind Kind, owner string) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to follow it", path)
	}
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, fmt.Sprintf(".authorized_keys.%d.tmp", os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(body)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	// ACL (or ownership) is applied to the temp inode before the rename, so
	// the destination is never missing and never briefly inherits a looser
	// DACL. os.Rename replaces an existing file, including on Windows.
	if err := finalize(tmp, kind, owner); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func parsePair(line string) (Record, bool) {
	typ, blob, comment, ok := splitKey(line)
	if !ok || !isPairComment(comment) {
		return Record{}, false
	}
	fields := strings.Fields(comment)
	if len(fields) < 3 || fields[0] != "kite-pair" {
		return Record{}, false
	}
	fp, err := Fingerprint(typ + " " + blob)
	if err != nil {
		return Record{}, false
	}
	return Record{
		Label:       fields[1],
		Date:        fields[2],
		Type:        typ,
		Fingerprint: fp,
		Line:        strings.TrimSpace(line),
	}, true
}

func isPairComment(comment string) bool {
	return strings.HasPrefix(comment, "kite-pair ")
}

func commentOf(line string) string {
	_, _, comment, ok := splitKey(line)
	if !ok {
		return ""
	}
	return comment
}

func keyBlob(line string) string {
	_, blob, _, ok := splitKey(line)
	if !ok {
		return ""
	}
	return blob
}

// splitKey finds the key type and blob. Lines may start with SSH options.
func splitKey(line string) (typ, blob, comment string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", "", false
	}
	fields := strings.Fields(line)
	for i := 0; i < len(fields); i++ {
		if isKeyType(fields[i]) && i+1 < len(fields) {
			typ = fields[i]
			blob = fields[i+1]
			comment = strings.Join(fields[i+2:], " ")
			return typ, blob, comment, true
		}
	}
	return "", "", "", false
}

func isKeyType(s string) bool {
	switch s {
	case "ssh-ed25519", "ssh-rsa", "ssh-dss", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	default:
		return strings.HasPrefix(s, "ssh-") || strings.HasPrefix(s, "ecdsa-")
	}
}

// Member reports whether user is listed by a Windows local-group query.
// Entries may be "MACHINE\\user" or just "user".
func Member(members []string, user string) bool {
	user = leaf(user)
	if user == "" {
		return false
	}
	for _, m := range members {
		if strings.EqualFold(leaf(m), user) {
			return true
		}
	}
	return false
}

func leaf(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// AdminACLArgs is the icacls invocation for administrators_authorized_keys.
// It grants SYSTEM and Administrators only, by well-known SID, and removes
// inherited ACEs. It does not open a firewall port.
func AdminACLArgs(path string) []string {
	return []string{
		"icacls", path,
		"/inheritance:r",
		"/grant:r", "*S-1-5-18:F",
		"/grant:r", "*S-1-5-32-544:F",
	}
}

// UserACLArgs tightens a per-user authorized_keys file.
func UserACLArgs(path, username string) []string {
	return []string{
		"icacls", path,
		"/inheritance:r",
		"/grant:r", leaf(username) + ":F",
		"/grant:r", "*S-1-5-18:F",
	}
}

// ContainsBlob reports whether body already has the key material in line.
func ContainsBlob(body []byte, line string) bool {
	want := keyBlob(line)
	if want == "" {
		return false
	}
	for _, ln := range bytes.Split(body, []byte("\n")) {
		if keyBlob(string(ln)) == want {
			return true
		}
	}
	return false
}
