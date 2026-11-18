//go:build !windows

package authkeys

// DetectAdmin reports whether username is a local administrator.
// Off Windows the administrators_authorized_keys file is not used.
func DetectAdmin(string) (bool, error) {
	return false, nil
}
