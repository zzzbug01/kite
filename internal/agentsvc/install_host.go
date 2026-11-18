//go:build !windows

package agentsvc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	hostBinary = "/usr/local/bin/kite-agent"
	hostEnv    = "/etc/kite/agent.env"
	hostUnit   = "/etc/systemd/system/kite-agent.service"
	hostPlist  = "/Library/LaunchDaemons/com.kite.agent.plist"
)

// Install on Linux and macOS runs after SSH setup. Root writes the supervisor
// files and starts the agent. Anyone else gets the exact manual commands.
func Install(opt Options) error {
	if os.Geteuid() != 0 {
		fmt.Fprint(os.Stdout, ManualInstallText(runtime.GOOS))
		return errors.New("kite-agent install needs root to register the service; re-run with sudo, or follow the manual steps above")
	}
	if err := rejectPlaceholderRelay(opt.Config.RelayAddr); err != nil {
		return err
	}
	body, err := AgentEnvFile(opt.Config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/kite", 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(hostEnv, []byte(body), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(hostEnv, 0o600); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(hostBinary), 0o755); err != nil {
		return err
	}
	if !sameFile(exe, hostBinary) {
		if err := copyBinary(hostBinary, exe); err != nil {
			return err
		}
	}
	switch runtime.GOOS {
	case "darwin":
		return installDarwin()
	default:
		return installSystemd()
	}
}

func installSystemd() error {
	if err := os.WriteFile(hostUnit, []byte(SystemdUnit()), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w (%s)", err, out)
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "kite-agent").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable --now kite-agent: %w (%s)", err, out)
	}
	fmt.Fprintln(os.Stdout, "kite-agent: systemd service enabled")
	fmt.Fprintln(os.Stdout, "config:", hostEnv)
	return nil
}

func installDarwin() error {
	if err := os.MkdirAll(filepath.Dir(hostPlist), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(hostPlist, []byte(LaunchDaemon()), 0o644); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", "system/com.kite.agent").Run()
	if out, err := exec.Command("launchctl", "bootstrap", "system", hostPlist).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("launchctl", "load", "-w", hostPlist).CombinedOutput(); err2 != nil {
			return fmt.Errorf("launchctl bootstrap: %w (%s); load: %v (%s)", err, out, err2, out2)
		}
	}
	fmt.Fprintln(os.Stdout, "kite-agent: launchd daemon bootstrapped")
	fmt.Fprintln(os.Stdout, "config:", hostEnv)
	return nil
}

func copyBinary(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func sameFile(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return aa == bb
}
