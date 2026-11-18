//go:build !windows

package agentsvc

import "context"

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

// Install registers the Windows service.
func Install(Options) error {
	return errWindowsOnly("install")
}

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

// Upgrade schedules a detached Windows binary swap.
func Upgrade(UpgradeOptions) error {
	return errWindowsOnly("upgrade")
}

// LogServiceError records a service-start failure in the Windows event log.
func LogServiceError(string, error) {}
