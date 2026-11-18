//go:build unix

package sshsetup

import "os"

func privileged() bool { return os.Geteuid() == 0 }
