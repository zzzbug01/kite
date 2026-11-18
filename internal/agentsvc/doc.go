// Package agentsvc installs and controls kite-agent as a Windows service.
//
// The service starts at boot, in session 0, so it does not open a console
// window and does not need a logged-on desktop. Commands other than the
// service itself are also exposed on macOS and Linux as clear errors that
// point at the existing systemd unit and launchd plist; those supervisors
// are unchanged.
package agentsvc
