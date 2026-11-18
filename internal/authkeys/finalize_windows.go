//go:build windows

package authkeys

import (
	"fmt"
	"os/exec"
)

func finalize(path string, kind Kind, owner string) error {
	var args []string
	switch kind {
	case KindWindowsAdmin:
		args = AdminACLArgs(path)
	default:
		if owner == "" {
			return nil
		}
		args = UserACLArgs(path, owner)
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", args[0], err, out)
	}
	return nil
}
