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
func Serve(func(context.Context) error) error {
	return errWindowsOnly("service")
}

// Install registers the Windows service.
func Install(Options) error {
	return errWindowsOnly("install")
}

// Uninstall removes the Windows service.
func Uninstall() error {
	return errWindowsOnly("uninstall")
}

// Start starts the Windows service.
func Start() error {
	return errWindowsOnly("start")
}

// Stop stops the Windows service.
func Stop() error {
	return errWindowsOnly("stop")
}

// Restart stops and starts the Windows service.
func Restart() error {
	return errWindowsOnly("restart")
}

// Status prints the Windows service state.
func Status() (string, error) {
	return "", errWindowsOnly("status")
}

// LogServiceError records a service-start failure in the Windows event log.
func LogServiceError(error) {}
