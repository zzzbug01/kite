//go:build windows

package agentsvc

import (
	"fmt"
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

func protectDir(path string) error {
	sddl, err := dirSDDL()
	if err != nil {
		return err
	}
	return setDACL(path, sddl)
}

func protectConfig(path string) error {
	sddl, err := configSDDL()
	if err != nil {
		return err
	}
	return setDACL(path, sddl)
}

func protectLog(path string) error {
	return setDACL(path, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)")
}

// dirSDDL lets SYSTEM, Administrators, and the current user manage the
// directory, and lets other users traverse it so they can open the log.
// The config file has its own DACL and does not inherit a users-read entry.
func dirSDDL() (string, error) {
	sid, err := currentSIDString()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;%s)(A;;GRGX;;;BU)", sid), nil
}

func configSDDL() (string, error) {
	sid, err := currentSIDString()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;%s)", sid), nil
}

func currentSIDString() (string, error) {
	// Pseudo-handle: do not close it.
	token := windows.GetCurrentProcessToken()
	tu, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	if tu.User.Sid == nil {
		return "", fmt.Errorf("token has no user SID")
	}
	s := tu.User.Sid.String()
	runtime.KeepAlive(tu)
	if s == "" {
		return "", fmt.Errorf("token user SID is empty")
	}
	return s, nil
}

func setDACL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return err
	}
	runtime.KeepAlive(sd)
	return nil
}

func replaceFile(tmp, dst string) error {
	from, err := syscall.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
