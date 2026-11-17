//go:build unix

package process

import (
	"os"
	"syscall"
)

// ShutdownSignals are the signals that should stop a kite process.
func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
