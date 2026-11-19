//go:build windows

package windir

import "golang.org/x/sys/windows"

func knownProgramFiles() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, 0); err == nil {
		if p != "" {
			return p
		}
	}
	if p, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0); err == nil {
		return p
	}
	return ""
}

func knownProgramData() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0); err == nil {
		return p
	}
	return ""
}
