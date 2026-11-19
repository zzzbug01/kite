//go:build !windows

package agentsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// RunningAsService reports whether this process was started by the Windows
// Service Control Manager. It is always false on other systems.
func RunningAsService() (bool, error) {
	return false, nil
}

// Serve runs the process as a Windows service. On other systems it explains
// the existing supervisor instead of installing one.
func Serve(string, func(context.Context) error) error {
	return errWindowsOnly("service")
}

// Install is provided by install_host.go on this platform.

// Uninstall disables the systemd unit or LaunchDaemon and leaves the env file.
func Uninstall(name string) error {
	canon, err := CanonicalServiceName(name)
	if err != nil {
		return err
	}
	lay := unixLayout(canon)
	if os.Geteuid() != 0 {
		return fmt.Errorf("kite-agent uninstall needs root. On Linux: sudo systemctl disable --now %s. On macOS: sudo launchctl bootout system/%s", lay.unitName, lay.label)
	}
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("launchctl", "bootout", "system/"+lay.label).Run()
		if err := os.Remove(lay.plist); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintf(os.Stdout, "Removed launchd daemon %s.\n", lay.label)
	default:
		if out, err := exec.Command("systemctl", "disable", "--now", lay.unitName).CombinedOutput(); err != nil {
			if !strings.Contains(string(out), "does not exist") && !strings.Contains(string(out), "not loaded") {
				return fmt.Errorf("systemctl disable --now %s: %w (%s)", lay.unitName, err, bytesTrim(out))
			}
		}
		if err := os.Remove(lay.unitPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command("systemctl", "daemon-reload").Run()
		fmt.Fprintf(os.Stdout, "Removed systemd unit %s.\n", lay.unitName)
	}
	fmt.Fprintf(os.Stdout, "The config was left on disk. Delete it to remove the agent token or registration key:\n  %s\n", lay.env)
	return nil
}

// Start starts the Windows service.
func Start(string) error {
	return errWindowsOnly("start")
}

// Stop stops the Windows service.
func Stop(string) error {
	return errWindowsOnly("stop")
}

// Restart stops and starts the Windows service.
func Restart(string) error {
	return errWindowsOnly("restart")
}

// Status prints the Windows service state.
func Status(string) (string, error) {
	return "", errWindowsOnly("status")
}

// List enumerates Windows kite-agent services. It is not available here.
func List() (string, error) {
	return "", fmt.Errorf("kite-agent list is not supported on %s; it lists Windows services named kite-agent and kite-agent-*", runtime.GOOS)
}

// RequireUpgradePriv is the root check for a download-and-replace upgrade.
// It runs before anything is written under /tmp.
func RequireUpgradePriv() error {
	if os.Geteuid() == 0 {
		return nil
	}
	return fmt.Errorf("kite-agent upgrade needs root to replace the service binary and restart it. Re-run: %s", sudoReRun())
}

// Upgrade replaces this instance's binary and restarts the supervisor.
// The Windows build schedules a SYSTEM task instead; --apply is that worker
// and only exists there.
func Upgrade(opt UpgradeOptions) error {
	if opt.Apply {
		return errWindowsOnly("upgrade --apply")
	}
	from := strings.TrimSpace(opt.From)
	if from == "" {
		return errors.New("no binary to install")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("kite-agent upgrade needs root to replace the service binary and restart it. On Linux that is systemctl restart; on macOS, launchctl kickstart. Re-run: %s", sudoReRun())
	}
	lay := unixLayout(opt.ServiceName)
	st, err := os.Stat(from)
	if err != nil {
		return fmt.Errorf("new binary: %w", err)
	}
	if st.IsDir() {
		return fmt.Errorf("new binary %s is a directory", from)
	}
	if err := os.MkdirAll(filepath.Dir(lay.bin), 0o755); err != nil {
		return err
	}
	if !sameFile(from, lay.bin) {
		if err := copyBinary(lay.bin, from); err != nil {
			return err
		}
	}
	if err := restartUnix(lay); err != nil {
		return fmt.Errorf("replaced %s but could not restart the service: %w", lay.bin, err)
	}
	fmt.Fprintf(os.Stdout, "Upgraded %s\n", lay.bin)
	return nil
}

func restartUnix(lay unixPaths) error {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("launchctl", "kickstart", "-k", "system/"+lay.label).CombinedOutput(); err != nil {
			if out2, err2 := exec.Command("launchctl", "bootstrap", "system", lay.plist).CombinedOutput(); err2 != nil {
				return fmt.Errorf("launchctl kickstart: %w (%s); bootstrap: %v (%s)", err, bytesTrim(out), err2, bytesTrim(out2))
			}
		}
		return nil
	default:
		if out, err := exec.Command("systemctl", "restart", lay.unitName).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl restart %s: %w (%s)", lay.unitName, err, bytesTrim(out))
		}
		return nil
	}
}

func bytesTrim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

// LogServiceError records a service-start failure in the Windows event log.
func LogServiceError(string, error) {}
