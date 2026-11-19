package agent

import (
	"os"
	"testing"
)

func TestIsMachineAccount(t *testing.T) {
	for _, name := range []string{"SYSTEM", "LocalService", "NetworkService", `NT AUTHORITY\SYSTEM`, "ZZZBUG$", `MACHINE\ZZZBUG$`} {
		if !IsMachineAccount(name) {
			t.Fatalf("%s should be a machine account", name)
		}
	}
	for _, name := range []string{"me", "Administrator", "alice"} {
		if IsMachineAccount(name) {
			t.Fatalf("%s is a person", name)
		}
	}
}

func TestInvokingUserPrefersSudoUser(t *testing.T) {
	t.Setenv("SUDO_USER", "alice")
	if got := InvokingUser(); got != "alice" {
		t.Fatalf("sudo user %q", got)
	}
	t.Setenv("SUDO_USER", `DOMAIN\ALICE$`)
	got := InvokingUser()
	if got == "ALICE$" || IsMachineAccount(got) {
		t.Fatalf("machine account leaked: %q", got)
	}
	if got == "" && os.Getenv("USER") == "root" {
		return
	}
}
