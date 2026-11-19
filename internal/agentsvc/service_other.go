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

// Uninstall removes the Windows service.
func Uninstall(string) error {
	return errWindowsOnly("uninstall")
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
		return errors.New("kite-agent upgrade needs root to replace the service binary and restart it. On Linux: sudo kite-agent upgrade (systemctl restart). On macOS: sudo kite-agent upgrade (launchctl kickstart)")
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
