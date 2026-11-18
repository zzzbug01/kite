//go:build windows

package authkeys

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DetectAdmin reports whether username is a member of the local
// Administrators group (SID S-1-5-32-544). The username is passed in the
// environment so it is not interpolated into the PowerShell script.
func DetectAdmin(username string) (bool, error) {
	username = leaf(username)
	if username == "" {
		return false, nil
	}
	script := `$ErrorActionPreference = 'Stop'
$want = $env:KITE_SSH_USER
$members = @(Get-LocalGroupMember -SID 'S-1-5-32-544' | ForEach-Object { $_.Name })
$hit = $false
foreach ($m in $members) {
  $leaf = $m
  $i = $m.LastIndexOf('\')
  if ($i -ge 0) { $leaf = $m.Substring($i + 1) }
  if ($leaf -eq $want) { $hit = $true }
}
if ($hit) { 'yes' } else { 'no' }
`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "KITE_SSH_USER="+username)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("detect administrators membership: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) == "yes", nil
}
