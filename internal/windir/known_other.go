//go:build !windows

package windir

func knownProgramFiles() string { return "" }
func knownProgramData() string  { return "" }
