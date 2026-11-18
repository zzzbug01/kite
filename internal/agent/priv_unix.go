//go:build unix

package agent

import "os"

func privilegedUser() bool { return os.Geteuid() == 0 }
