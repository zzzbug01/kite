package agent

import "testing"

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
