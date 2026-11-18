// Package sshsetup checks the local SSH server and, when this process is
// privileged, installs and starts it. kite-agent dials 127.0.0.1:22 only, so
// this package never adds a firewall rule.
package sshsetup

import (
	"fmt"
	"strings"
)

// Command is one action Ensure may run. Display is what we print. Argv is
// what we execute, with no shell interpolation.
type Command struct {
	Display string
	Argv    []string
}

// Snapshot is a point-in-time view of the local SSH server.
type Snapshot struct {
	GOOS          string
	Privileged    bool
	LocalPortOpen bool
	Addr          string

	// Linux
	Family         string // apt, dnf, yum, pacman, apk, ""
	HasSSHD        bool
	Service        string // ssh or sshd
	ServiceActive  bool
	ServiceEnabled bool
	HasSystemctl   bool
	HasOpenRC      bool

	// Windows
	Installed bool
	StartType string // Automatic, Manual, Disabled, ""
	Status    string // Running, Stopped, ""

	// macOS: on, off, unknown
	RemoteLogin string
}

// Report is the human-readable result of a plan.
type Report struct {
	Lines    []string
	Commands []Command
	// Pending is true when commands are required and this process cannot run them.
	Pending bool
}

// Text renders status lines and, when commands remain, the exact commands.
func (r Report) Text() string {
	var b strings.Builder
	for _, line := range r.Lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// Plan decides what to print and which commands to run. It does not execute
// anything. A second plan against the state the commands would produce has
// no commands.
func Plan(s Snapshot) (Report, error) {
	if s.Addr == "" {
		s.Addr = "127.0.0.1:22"
	}
	switch s.GOOS {
	case "windows":
		return planWindows(s)
	case "darwin":
		return planDarwin(s)
	default:
		return planLinux(s)
	}
}

func planWindows(s Snapshot) (Report, error) {
	var r Report
	r.Lines = append(r.Lines, "firewall: unchanged (kite dials 127.0.0.1:22 only; no inbound rule added)")
	if !s.Installed {
		r.Lines = append(r.Lines, "sshd: OpenSSH Server is not installed")
		cmd := Command{
			Display: "Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0",
			Argv:    powershell("Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0"),
		}
		r.Commands = append(r.Commands, cmd)
		r.Commands = append(r.Commands, setAutomatic(), startSSHD())
		return finish(r, s.Privileged, "OpenSSH Server is not installed")
	}
	r.Lines = append(r.Lines, "sshd: installed")
	start := strings.ToLower(strings.TrimSpace(s.StartType))
	if start != "automatic" {
		was := s.StartType
		if was == "" {
			was = "unset"
		}
		r.Lines = append(r.Lines, "sshd: startup set to Automatic (was "+was+")")
		r.Commands = append(r.Commands, setAutomatic())
	} else {
		r.Lines = append(r.Lines, "sshd: startup Automatic")
	}
	if !strings.EqualFold(s.Status, "Running") {
		r.Lines = append(r.Lines, "sshd: starting")
		r.Commands = append(r.Commands, startSSHD())
	} else {
		r.Lines = append(r.Lines, "sshd: running")
	}
	if len(r.Commands) == 0 {
		return r, nil
	}
	return finish(r, s.Privileged, "sshd is not ready")
}

func setAutomatic() Command {
	return Command{
		Display: "Set-Service -Name sshd -StartupType Automatic",
		Argv:    powershell("Set-Service -Name sshd -StartupType Automatic"),
	}
}

func startSSHD() Command {
	return Command{
		Display: "Start-Service sshd",
		Argv:    powershell("Start-Service sshd"),
	}
}

func powershell(script string) []string {
	return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script}
}

func planLinux(s Snapshot) (Report, error) {
	var r Report
	svc := s.Service
	if svc == "" {
		if s.Family == "apt" {
			svc = "ssh"
		} else {
			svc = "sshd"
		}
	}
	if !s.HasSSHD {
		display, argv, ok := installCommand(s.Family)
		r.Lines = append(r.Lines, "openssh-server: not installed")
		if !ok {
			r.Lines = append(r.Lines, "could not detect apt, dnf, pacman, or apk from /etc/os-release")
			r.Lines = append(r.Lines, "install OpenSSH and enable sshd, then re-run kite-agent setup")
			r.Pending = true
			return r, fmt.Errorf("openssh-server is not installed and the distro is unknown")
		}
		r.Lines = append(r.Lines, "install: "+display)
		r.Commands = append(r.Commands, Command{Display: display, Argv: argv})
		r.Commands = append(r.Commands, enableCommand(s, svc)...)
		return finish(r, s.Privileged, "openssh-server is not installed")
	}
	r.Lines = append(r.Lines, "openssh-server: installed ("+emptyFamily(s.Family)+")")
	if s.ServiceActive && s.ServiceEnabled {
		r.Lines = append(r.Lines, svc+": enabled")
		r.Lines = append(r.Lines, svc+": active")
		return r, nil
	}
	if !s.HasSystemctl && !s.HasOpenRC {
		if s.LocalPortOpen {
			r.Lines = append(r.Lines, "sshd: listening on "+s.Addr)
			r.Lines = append(r.Lines, "sshd: no systemd or openrc found; enable it at boot yourself")
			return r, nil
		}
		r.Lines = append(r.Lines, "sshd: installed but not detected as running, and no service manager was found")
		r.Pending = true
		return r, fmt.Errorf("sshd is not running")
	}
	if !s.ServiceEnabled {
		r.Lines = append(r.Lines, svc+": enabling")
	} else {
		r.Lines = append(r.Lines, svc+": enabled")
	}
	if !s.ServiceActive {
		r.Lines = append(r.Lines, svc+": starting")
	}
	r.Commands = append(r.Commands, enableCommand(s, svc)...)
	return finish(r, s.Privileged, "sshd service is not enabled and running")
}

func emptyFamily(f string) string {
	if f == "" {
		return "detected"
	}
	return f
}

func installCommand(family string) (string, []string, bool) {
	switch family {
	case "apt":
		return "apt-get install -y openssh-server", []string{"apt-get", "install", "-y", "openssh-server"}, true
	case "dnf":
		return "dnf install -y openssh-server", []string{"dnf", "install", "-y", "openssh-server"}, true
	case "yum":
		return "yum install -y openssh-server", []string{"yum", "install", "-y", "openssh-server"}, true
	case "pacman":
		return "pacman -S --noconfirm openssh", []string{"pacman", "-S", "--noconfirm", "openssh"}, true
	case "apk":
		return "apk add openssh", []string{"apk", "add", "openssh"}, true
	default:
		return "", nil, false
	}
}

func enableCommand(s Snapshot, svc string) []Command {
	if s.HasOpenRC && !s.HasSystemctl {
		return []Command{
			{Display: "rc-update add " + svc + " boot", Argv: []string{"rc-update", "add", svc, "boot"}},
			{Display: "rc-service " + svc + " start", Argv: []string{"rc-service", svc, "start"}},
		}
	}
	display := "systemctl enable --now " + svc
	return []Command{{Display: display, Argv: []string{"systemctl", "enable", "--now", svc}}}
}

func planDarwin(s Snapshot) (Report, error) {
	var r Report
	on := strings.EqualFold(s.RemoteLogin, "on") || (s.RemoteLogin != "off" && s.LocalPortOpen)
	if on {
		r.Lines = append(r.Lines, "Remote Login: on")
		return r, nil
	}
	r.Lines = append(r.Lines, "Remote Login: off")
	r.Commands = append(r.Commands, Command{
		Display: "systemsetup -setremotelogin on",
		Argv:    []string{"systemsetup", "-setremotelogin", "on"},
	})
	return finish(r, s.Privileged, "Remote Login is off")
}

func finish(r Report, privileged bool, why string) (Report, error) {
	if len(r.Commands) == 0 {
		return r, nil
	}
	if privileged {
		return r, nil
	}
	r.Pending = true
	r.Lines = append(r.Lines, "re-run as root to apply, or run:")
	for _, c := range r.Commands {
		r.Lines = append(r.Lines, "  sudo "+c.Display)
	}
	return r, fmt.Errorf("%s", why)
}

// FamilyFromOSRelease maps /etc/os-release ID and ID_LIKE to a package manager.
func FamilyFromOSRelease(text string) string {
	id, like := parseOSRelease(text)
	return family(id, like)
}

func parseOSRelease(text string) (id string, like []string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"'`)
		switch key {
		case "ID":
			id = strings.ToLower(val)
		case "ID_LIKE":
			like = strings.Fields(strings.ToLower(val))
		}
	}
	return id, like
}

func family(id string, like []string) string {
	names := append([]string{id}, like...)
	for _, n := range names {
		switch n {
		case "debian", "ubuntu", "linuxmint", "pop", "raspbian":
			return "apt"
		case "fedora", "rhel", "centos", "rocky", "almalinux", "ol":
			return "dnf"
		case "arch", "manjaro":
			return "pacman"
		case "alpine":
			return "apk"
		}
	}
	return ""
}

// ParseWindowsStatus reads the machine-readable sshd probe.
func ParseWindowsStatus(out string) (installed bool, startType, status string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "INSTALLED":
			installed = strings.EqualFold(v, "yes")
		case "START":
			startType = v
		case "STATUS":
			status = v
		}
	}
	return installed, startType, status
}

// ParseRemoteLogin reads `systemsetup -getremotelogin` output.
func ParseRemoteLogin(out string) string {
	low := strings.ToLower(out)
	if strings.Contains(low, "remote login: on") {
		return "on"
	}
	if strings.Contains(low, "remote login: off") {
		return "off"
	}
	return "unknown"
}
