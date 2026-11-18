package agentsvc

import (
	"fmt"
	"strings"

	"github.com/zzzbug01/kite/internal/config"
)

// AgentEnvFile is the systemd/launchd environment file. Values with spaces
// are single-quoted. A newline in a value is rejected so the registration
// key cannot break out of its line.
func AgentEnvFile(a config.Agent) (string, error) {
	var b strings.Builder
	b.WriteString("# kite-agent environment. Contains the registration key. Do not commit.\n")
	rows := [][2]string{
		{"RELAY_ADDR", a.RelayAddr},
		{"REGISTRATION_KEY", a.RegistrationKey},
		{"AGENT_NAME", a.Name},
		{"LOCAL_SSH_ADDR", a.LocalSSHAddr},
		{"IDLE_TIMEOUT", formatDuration(a.IdleTimeout)},
	}
	if strings.TrimSpace(a.SSHUser) != "" {
		rows = append(rows, [2]string{"SSH_USER", a.SSHUser})
	}
	for _, row := range rows {
		quoted, err := quoteEnv(row[1])
		if err != nil {
			return "", fmt.Errorf("%s: %w", row[0], err)
		}
		fmt.Fprintf(&b, "%s=%s\n", row[0], quoted)
	}
	return b.String(), nil
}

func quoteEnv(v string) (string, error) {
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("value contains a newline")
	}
	if v == "" || strings.ContainsAny(v, " \t#'\"\\$") {
		if strings.Contains(v, "'") {
			return "", fmt.Errorf("value contains a single quote; set it another way")
		}
		return "'" + v + "'", nil
	}
	return v, nil
}

// SystemdUnit is the unit install writes to /etc/systemd/system/kite-agent.service.
// It matches deploy/kite-agent.service.
func SystemdUnit() string {
	return `[Unit]
Description=kite agent (dial-out SSH tunnel)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/kite-agent
EnvironmentFile=/etc/kite/agent.env
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`
}

// LaunchDaemon is the system LaunchDaemon install writes on macOS.
// It reads /etc/kite/agent.env and does not embed the registration key.
func LaunchDaemon() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.kite.agent</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/sh</string>
        <string>-lc</string>
        <string>set -a; . /etc/kite/agent.env; set +a; exec /usr/local/bin/kite-agent</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>Comment</key>
    <string>kite-agent. The registration key lives in /etc/kite/agent.env. Do not commit that file.</string>
</dict>
</plist>
`
}

// ManualInstallText is printed when install is not running as root.
func ManualInstallText(goos string) string {
	switch goos {
	case "darwin":
		return `SSH setup is done when the lines above say Remote Login is on.
To install the agent without root, copy deploy/com.kite.agent.plist into
~/Library/LaunchAgents and put the registration key in ~/.config/kite/agent.env.
To install it as a system daemon, re-run: sudo kite-agent install
`
	default:
		return `SSH setup is done when the lines above say the ssh service is active.
To install the agent, re-run as root:

  sudo kite-agent install

Or copy deploy/kite-agent.service to /etc/systemd/system, put the registration
key in /etc/kite/agent.env, and run: sudo systemctl enable --now kite-agent
`
	}
}
