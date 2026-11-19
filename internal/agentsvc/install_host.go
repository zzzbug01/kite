//go:build !windows

package agentsvc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/zzzbug01/kite/internal/agentctl"
)

// unixPaths is one Linux or macOS instance. The default name keeps the
// paths the deploy files already use. Any other name gets its own env file,
// unit, and state directory under /var/lib/kite-agent, not /var/lib/kite.
type unixPaths struct {
	name     string
	bin      string
	env      string
	unitName string
	unitPath string
	state    string
	plist    string
	label    string
}

func unixLayout(name string) unixPaths {
	if name == "" || name == ServiceName {
		return unixPaths{
			name:     ServiceName,
			bin:      agentctl.UnixBinary(ServiceName),
			env:      agentctl.UnixEnvFile(ServiceName),
			unitName: ServiceName,
			unitPath: "/etc/systemd/system/kite-agent.service",
			state:    agentctl.UnixStateDir(ServiceName),
			plist:    "/Library/LaunchDaemons/com.kite.agent.plist",
			label:    "com.kite.agent",
		}
	}
	return unixPaths{
		name:     name,
		bin:      agentctl.UnixBinary(name),
		env:      agentctl.UnixEnvFile(name),
		unitName: name,
		unitPath: filepath.Join("/etc/systemd/system", name+".service"),
		state:    agentctl.UnixStateDir(name),
		plist:    filepath.Join("/Library/LaunchDaemons", "com.kite."+name+".plist"),
		label:    "com.kite." + name,
	}
}

// Preflight reports whether install can register the service. It does not
// redeem an install code and does not change the machine.
func Preflight(opt Options) error {
	if os.Geteuid() == 0 {
		return nil
	}
	return errors.New("kite-agent install needs root to register the service. The install code was not used. Re-run: " + sudoReRun())
}

// Install on Linux and macOS runs after SSH setup. Root writes the supervisor
// files and starts the agent. Anyone else gets the exact manual commands.
func Install(opt Options) error {
	if err := Preflight(opt); err != nil {
		fmt.Fprint(os.Stdout, ManualInstallText(runtime.GOOS))
		return err
	}
	if err := rejectPlaceholderRelay(opt.Config.RelayAddr); err != nil {
		return err
	}
	lay := unixLayout(opt.ServiceName)
	body, err := AgentEnvFile(opt.Config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lay.env), 0o700); err != nil {
		return err
	}
	// 0711 so a named instance under /var/lib/kite-agent/<name> can be
	// reached before a default instance has chmod'd the parent.
	if err := agentctl.PrepareStateDir(lay.state); err != nil {
		return err
	}
	if err := os.WriteFile(lay.env, []byte(body), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(lay.env, 0o600); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lay.bin), 0o755); err != nil {
		return err
	}
	if !sameFile(exe, lay.bin) {
		if err := copyBinary(lay.bin, exe); err != nil {
			return err
		}
	}
	switch runtime.GOOS {
	case "darwin":
		return installDarwin(lay)
	default:
		return installSystemd(lay)
	}
}

func installSystemd(lay unixPaths) error {
	unit := SystemdUnit()
	if lay.name != ServiceName {
		unit = systemdUnitNamed(lay)
	}
	if err := os.WriteFile(lay.unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w (%s)", err, out)
	}
	if out, err := exec.Command("systemctl", "enable", "--now", lay.unitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable --now %s: %w (%s)", lay.unitName, err, out)
	}
	fmt.Fprintf(os.Stdout, "kite-agent: systemd service %s enabled\n", lay.unitName)
	fmt.Fprintln(os.Stdout, "config:", lay.env)
	fmt.Fprintln(os.Stdout, "state:", lay.state)
	return nil
}

func systemdUnitNamed(lay unixPaths) string {
	return "[Unit]\n" +
		"Description=kite agent (dial-out SSH tunnel)\n" +
		"After=network-online.target\n" +
		"Wants=network-online.target\n\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + lay.bin + " --service-name " + lay.name + "\n" +
		"EnvironmentFile=" + lay.env + "\n" +
		"Restart=always\n" +
		"RestartSec=5\n" +
		"NoNewPrivileges=true\n" +
		"PrivateTmp=true\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}

func installDarwin(lay unixPaths) error {
	if err := os.MkdirAll(filepath.Dir(lay.plist), 0o755); err != nil {
		return err
	}
	body := LaunchDaemon()
	if lay.name != ServiceName {
		body = launchDaemonNamed(lay)
	}
	if err := os.WriteFile(lay.plist, []byte(body), 0o644); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", "system/"+lay.label).Run()
	if out, err := exec.Command("launchctl", "bootstrap", "system", lay.plist).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("launchctl", "load", "-w", lay.plist).CombinedOutput(); err2 != nil {
			return fmt.Errorf("launchctl bootstrap: %w (%s); load: %v (%s)", err, out, err2, out2)
		}
	}
	fmt.Fprintf(os.Stdout, "kite-agent: launchd daemon %s bootstrapped\n", lay.label)
	fmt.Fprintln(os.Stdout, "config:", lay.env)
	fmt.Fprintln(os.Stdout, "state:", lay.state)
	return nil
}

func launchDaemonNamed(lay unixPaths) string {
	script := "set -a; . " + lay.env + "; set +a; exec " + lay.bin + " --service-name " + lay.name
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>` + lay.label + `</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/sh</string>
        <string>-lc</string>
        <string>` + script + `</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`
}

func copyBinary(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func sameFile(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return aa == bb
}
