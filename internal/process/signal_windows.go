//go:build windows

package process

import "os"

// ShutdownSignals are the signals that should stop a kite process.
func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
