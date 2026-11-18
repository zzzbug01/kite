//go:build windows

package clientkey

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// tighten leaves only the current user on the private key. OpenSSH refuses
// a key that Administrators and Users can both read.
func tighten(path string) error {
	_ = os.Chmod(path, 0o600)
	sid, err := currentSID()
	if err != nil {
		return err
	}
	out, err := exec.Command("icacls", path, "/inheritance:r", "/grant:r", "*"+sid+":F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls %s: %w (%s)", path, err, out)
	}
	return nil
}

func currentSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	tu, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	if tu.User.Sid == nil {
		return "", fmt.Errorf("token has no user SID")
	}
	return tu.User.Sid.String(), nil
}
