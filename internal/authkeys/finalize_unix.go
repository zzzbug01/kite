//go:build unix

package authkeys

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
)

func finalize(path string, kind Kind, owner string) error {
	if os.Geteuid() != 0 || strings.TrimSpace(owner) == "" {
		return nil
	}
	uid, gid, err := lookupIDs(owner)
	if err != nil {
		return err
	}
	dir := path
	if kind != KindWindowsAdmin {
		dir = filepathDir(path)
		if err := os.Chown(dir, uid, gid); err != nil {
			return err
		}
	}
	return os.Chown(path, uid, gid)
}

func filepathDir(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "."
	}
	return path[:i]
}

func lookupIDs(username string) (int, int, error) {
	username = leaf(username)
	if u, err := user.Lookup(username); err == nil {
		uid, uerr := strconv.Atoi(u.Uid)
		gid, gerr := strconv.Atoi(u.Gid)
		if uerr == nil && gerr == nil {
			return uid, gid, nil
		}
	}
	uidOut, err := exec.Command("id", "-u", username).Output()
	if err != nil {
		return 0, 0, err
	}
	gidOut, err := exec.Command("id", "-g", username).Output()
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(uidOut)))
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(strings.TrimSpace(string(gidOut)))
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}
