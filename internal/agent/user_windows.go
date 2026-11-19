//go:build windows

package agent

import "golang.org/x/sys/windows"

// consoleUser is the account logged on at the active console. It is used
// when this process is LocalSystem or another machine account, which happens
// for the Windows service and for some elevated relaunches. WTSQueryUserToken
// succeeds for LocalSystem and fails for a normal elevated user; the caller
// already prefers user.Current in that case.
func consoleUser() string {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF {
		return ""
	}
	var tok windows.Token
	if err := windows.WTSQueryUserToken(session, &tok); err != nil {
		return ""
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil || tu == nil || tu.User.Sid == nil {
		return ""
	}
	account, _, _, err := tu.User.Sid.LookupAccount("")
	if err != nil {
		return ""
	}
	return account
}
