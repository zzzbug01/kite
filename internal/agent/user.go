package agent

import (
	"os"
	"os/user"
	"strings"
)

// InvokingUser is the person who started this process.
// Under sudo that is SUDO_USER. On Windows a machine account such as HOST$
// or SYSTEM is never returned; the active console user is used instead.
// root is not used, so a service that is actually root leaves the SSH user
// unset until a person runs pair.
func InvokingUser() string {
	if v := strings.TrimSpace(os.Getenv("SUDO_USER")); v != "" {
		v = leafName(v)
		if v != "" && v != "root" && !IsMachineAccount(v) {
			return v
		}
	}
	if u, err := user.Current(); err == nil {
		name := leafName(u.Username)
		if name != "" && name != "root" && !IsMachineAccount(name) {
			return name
		}
	}
	if name := leafName(consoleUser()); name != "" && name != "root" && !IsMachineAccount(name) {
		return name
	}
	return ""
}
