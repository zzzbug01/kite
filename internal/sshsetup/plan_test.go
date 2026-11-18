package sshsetup

import (
	"strings"
	"testing"
)

func TestWindowsAutomaticEvenWhenRunning(t *testing.T) {
	rep, err := Plan(Snapshot{
		GOOS: "windows", Privileged: true, Installed: true,
		StartType: "Manual", Status: "Running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Commands) != 1 || !strings.Contains(rep.Commands[0].Display, "StartupType Automatic") {
		t.Fatalf("%+v", rep.Commands)
	}
	text := rep.Text()
	if !strings.Contains(text, "was Manual") || !strings.Contains(text, "firewall: unchanged") {
		t.Fatal(text)
	}
	assertNoFirewall(t, rep)
}

func TestWindowsIdempotent(t *testing.T) {
	rep, err := Plan(Snapshot{
		GOOS: "windows", Privileged: true, Installed: true,
		StartType: "Automatic", Status: "Running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Commands) != 0 {
		t.Fatalf("expected no commands, got %+v", rep.Commands)
	}
	if !strings.Contains(rep.Text(), "startup Automatic") || !strings.Contains(rep.Text(), "sshd: running") {
		t.Fatal(rep.Text())
	}
}

func TestWindowsInstallWhenMissing(t *testing.T) {
	rep, err := Plan(Snapshot{GOOS: "windows", Privileged: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Commands) != 3 {
		t.Fatalf("%+v", rep.Commands)
	}
	if !strings.Contains(rep.Commands[0].Display, "OpenSSH.Server~~~~0.0.1.0") {
		t.Fatal(rep.Commands[0].Display)
	}
	assertNoFirewall(t, rep)

	rep, err = Plan(Snapshot{GOOS: "windows", Privileged: false})
	if err == nil {
		t.Fatal("expected an error when not privileged")
	}
	if !rep.Pending || !strings.Contains(rep.Text(), "sudo Add-WindowsCapability") && !strings.Contains(rep.Text(), "Add-WindowsCapability") {
		t.Fatal(rep.Text())
	}
	assertNoFirewall(t, rep)
}

func TestLinuxCommandsByDistro(t *testing.T) {
	cases := []struct {
		release string
		want    string
		svc     string
	}{
		{"ID=ubuntu\nID_LIKE=debian\n", "apt-get install -y openssh-server", "ssh"},
		{"ID=debian\n", "apt-get install -y openssh-server", "ssh"},
		{"ID=raspbian\nID_LIKE=debian\n", "apt-get install -y openssh-server", "ssh"},
		{"ID=fedora\n", "dnf install -y openssh-server", "sshd"},
		{"ID=almalinux\nID_LIKE=\"rhel centos fedora\"\n", "dnf install -y openssh-server", "sshd"},
		{"ID=arch\n", "pacman -S --noconfirm openssh", "sshd"},
		{"ID=alpine\n", "apk add openssh", "sshd"},
	}
	for _, tc := range cases {
		fam := FamilyFromOSRelease(tc.release)
		rep, err := Plan(Snapshot{
			GOOS: "linux", Privileged: false, Family: fam, HasSystemctl: true,
		})
		if err == nil {
			t.Fatalf("%s: expected pending error", tc.release)
		}
		if len(rep.Commands) == 0 || rep.Commands[0].Display != tc.want {
			t.Fatalf("%s: %+v", tc.release, rep.Commands)
		}
		joined := rep.Text()
		if !strings.Contains(joined, "sudo "+tc.want) {
			t.Fatalf("%s:\n%s", tc.release, joined)
		}
		found := false
		for _, c := range rep.Commands {
			if strings.Contains(c.Display, tc.svc) && strings.Contains(c.Display, "enable") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s service %s not in %+v", tc.release, tc.svc, rep.Commands)
		}
	}
}

func TestLinuxAlreadyReady(t *testing.T) {
	rep, err := Plan(Snapshot{
		GOOS: "linux", Privileged: false, Family: "apt", HasSSHD: true,
		Service: "ssh", ServiceActive: true, ServiceEnabled: true, HasSystemctl: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Commands) != 0 {
		t.Fatal(rep.Commands)
	}
	if !strings.Contains(rep.Text(), "ssh: enabled") || !strings.Contains(rep.Text(), "ssh: active") {
		t.Fatal(rep.Text())
	}
}

func TestLinuxEnablesWhenRoot(t *testing.T) {
	rep, err := Plan(Snapshot{
		GOOS: "linux", Privileged: true, Family: "apt", HasSSHD: true,
		Service: "ssh", HasSystemctl: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Commands) != 1 || rep.Commands[0].Display != "systemctl enable --now ssh" {
		t.Fatalf("%+v", rep.Commands)
	}
}

func TestOpenRC(t *testing.T) {
	rep, err := Plan(Snapshot{
		GOOS: "linux", Privileged: true, Family: "apk", HasSSHD: true,
		Service: "sshd", HasOpenRC: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Commands) != 2 {
		t.Fatalf("%+v", rep.Commands)
	}
	if rep.Commands[0].Argv[0] != "rc-update" || rep.Commands[1].Argv[0] != "rc-service" {
		t.Fatal(rep.Commands)
	}
}

func TestDarwin(t *testing.T) {
	rep, err := Plan(Snapshot{GOOS: "darwin", Privileged: true, RemoteLogin: "on"})
	if err != nil || len(rep.Commands) != 0 || !strings.Contains(rep.Text(), "Remote Login: on") {
		t.Fatalf("%v %s", err, rep.Text())
	}
	rep, err = Plan(Snapshot{GOOS: "darwin", Privileged: true, RemoteLogin: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Commands) != 1 || rep.Commands[0].Display != "systemsetup -setremotelogin on" {
		t.Fatal(rep.Commands)
	}
	rep, err = Plan(Snapshot{GOOS: "darwin", Privileged: false, RemoteLogin: "off"})
	if err == nil || !strings.Contains(rep.Text(), "sudo systemsetup -setremotelogin on") {
		t.Fatalf("%v\n%s", err, rep.Text())
	}
	// A port that is already open counts as on when systemsetup could not be read.
	rep, err = Plan(Snapshot{GOOS: "darwin", Privileged: false, RemoteLogin: "unknown", LocalPortOpen: true})
	if err != nil || len(rep.Commands) != 0 {
		t.Fatalf("%v %+v", err, rep.Commands)
	}
}

func TestIdempotentAfterCommands(t *testing.T) {
	// The state a successful Windows fix leaves behind plans no further commands.
	after, err := Plan(Snapshot{
		GOOS: "windows", Privileged: true, Installed: true,
		StartType: "Automatic", Status: "Running",
	})
	if err != nil || len(after.Commands) != 0 {
		t.Fatalf("%v %+v", err, after.Commands)
	}
	linuxAfter, err := Plan(Snapshot{
		GOOS: "linux", Privileged: true, Family: "dnf", HasSSHD: true,
		Service: "sshd", ServiceActive: true, ServiceEnabled: true, HasSystemctl: true,
	})
	if err != nil || len(linuxAfter.Commands) != 0 {
		t.Fatalf("%v %+v", err, linuxAfter.Commands)
	}
}

func TestParseHelpers(t *testing.T) {
	inst, start, status := ParseWindowsStatus("INSTALLED=yes\nSTART=Manual\nSTATUS=Running\n")
	if !inst || start != "Manual" || status != "Running" {
		t.Fatalf("%v %s %s", inst, start, status)
	}
	if ParseRemoteLogin("Remote Login: On\n") != "on" {
		t.Fatal("on")
	}
	if ParseRemoteLogin("Remote Login: Off\n") != "off" {
		t.Fatal("off")
	}
	if FamilyFromOSRelease("ID=linuxmint\nID_LIKE=ubuntu debian\n") != "apt" {
		t.Fatal("mint")
	}
}

func assertNoFirewall(t *testing.T, rep Report) {
	t.Helper()
	for _, c := range rep.Commands {
		blob := strings.ToLower(c.Display + " " + strings.Join(c.Argv, " "))
		if strings.Contains(blob, "firewall") || strings.Contains(blob, "new-netfirewallrule") || strings.Contains(blob, "port 22") {
			t.Fatalf("firewall command: %+v", c)
		}
	}
}
