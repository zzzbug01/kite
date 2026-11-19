// Package windir resolves the Windows Program Files and ProgramData
// directories the same way for the agent and the installers.
//
// A 32-bit process sees ProgramFiles as "Program Files (x86)". ProgramW6432
// stays the 64-bit folder, which is where kite installs. The environment
// wins over the Known Folder so a test or a relocated install is honored.
package windir

import (
	"os"
	"strings"
)

// ProgramFiles is the 64-bit Program Files directory.
func ProgramFiles() string {
	return first(
		os.Getenv("ProgramW6432"),
		os.Getenv("ProgramFiles"),
		knownProgramFiles(),
		`C:\Program Files`,
	)
}

// ProgramData is the machine-wide ProgramData directory.
func ProgramData() string {
	return first(
		os.Getenv("ProgramData"),
		knownProgramData(),
		`C:\ProgramData`,
	)
}

func first(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}
