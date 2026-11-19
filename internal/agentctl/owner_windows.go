//go:build windows

package agentctl

// ShareControlFile is a no-op on Windows. The service config ACL already
// grants the installing user read access.
func ShareControlFile(string, string) error { return nil }
