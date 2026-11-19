//go:build windows

package agentsvc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Upgrade replaces one installed kite-agent.exe without doing the stop in
// the caller's session. The caller only registers a SYSTEM scheduled task
// and starts it. The task keeps running if that session is torn down when
// the tunnel restarts.
func Upgrade(opt UpgradeOptions) error {
	if opt.Apply {
		return applyUpgrade(opt)
	}
	return scheduleUpgrade(opt)
}

func scheduleUpgrade(opt UpgradeOptions) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	name, err := CanonicalServiceName(opt.ServiceName)
	if err != nil {
		return err
	}
	from, err := absFile(opt.From)
	if err != nil {
		return err
	}
	m, err := connectSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := openService(m, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("service %s is not installed", name)
		}
		return err
	}
	s.Close()

	lay, err := DefaultLayout(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lay.ConfigPath), 0o755); err != nil {
		return err
	}
	result := UpgradeResultFile(lay)
	appendUpgradeLog(result, "scheduling upgrade of "+name+" from "+from)
	xmlText, err := UpgradeTaskXML(from, from, name)
	if err != nil {
		appendUpgradeLog(result, "upgrade failed: "+err.Error())
		return err
	}
	xmlPath := filepath.Join(filepath.Dir(lay.ConfigPath), "upgrade-task.xml")
	if err := writeUTF16File(xmlPath, xmlText); err != nil {
		appendUpgradeLog(result, "upgrade failed: "+err.Error())
		return err
	}
	task := UpgradeTaskName(name)
	if err := schtasks("/Create", "/TN", task, "/XML", xmlPath, "/F"); err != nil {
		_ = os.Remove(xmlPath)
		appendUpgradeLog(result, "register upgrade task failed: "+err.Error())
		return fmt.Errorf("register upgrade task: %w", err)
	}
	fmt.Printf("Scheduled upgrade of service %s.\n", name)
	fmt.Printf("New binary: %s\n", from)
	fmt.Printf("A one-shot SYSTEM task (%s) stops the service, replaces the exe, and starts it.\n", task)
	fmt.Printf("This session can disconnect when the tunnel restarts. The task still finishes.\n")
	fmt.Printf("Result: %s\n", result)
	os.Stdout.Sync()
	before := int64(0)
	if st, err := os.Stat(result); err == nil {
		before = st.Size()
	}
	if err := schtasks("/Run", "/TN", task); err != nil {
		_ = schtasks("/Delete", "/TN", task, "/F")
		_ = os.Remove(xmlPath)
		appendUpgradeLog(result, "start upgrade task failed: "+err.Error())
		return fmt.Errorf("start upgrade task: %w", err)
	}
	appendUpgradeLog(result, "upgrade task started: "+task)
	// Wait for the task. A failed swap used to leave the service stopped
	// while this command exited 0, and the installer then printed success.
	if err := waitUpgradeResult(result, before, 60*time.Second); err != nil {
		return err
	}
	removePrivateStage(from)
	return nil
}

func applyUpgrade(opt UpgradeOptions) error {
	name, err := CanonicalServiceName(opt.ServiceName)
	if err != nil {
		return err
	}
	from, err := absFile(opt.From)
	if err != nil {
		return err
	}
	lay, err := DefaultLayout(name)
	if err != nil {
		return err
	}
	result := UpgradeResultFile(lay)
	note := func(msg string) {
		appendUpgradeLog(result, msg)
	}
	lock := filepath.Join(filepath.Dir(lay.ConfigPath), "upgrade.lock")
	release, err := takeUpgradeLock(lock)
	if err != nil {
		if err == errUpgradeBusy {
			note("upgrade already running for " + name)
			return nil
		}
		return err
	}
	defer release()
	task := UpgradeTaskName(name)
	// Delete the task after the swap, including on failure, so it cannot run again.
	defer func() {
		_ = schtasks("/Delete", "/TN", task, "/F")
		_ = os.Remove(filepath.Join(filepath.Dir(lay.ConfigPath), "upgrade-task.xml"))
	}()

	note("stopping " + name)
	if err := requireAdmin(); err != nil {
		note("upgrade failed: " + err.Error())
		return err
	}
	m, err := connectSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		note("upgrade failed: " + err.Error())
		return err
	}
	defer m.Disconnect()
	access := uint32(windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG | windows.SERVICE_START | windows.SERVICE_STOP)
	s, err := openService(m, name, access)
	if err != nil {
		note("upgrade failed: " + err.Error())
		return err
	}
	defer s.Close()
	info, err := readStatus(s)
	if err != nil {
		note("upgrade failed: " + err.Error())
		return err
	}
	dest := info.Binary
	if dest == "" {
		dest = filepath.Join(lay.InstallDir, "kite-agent.exe")
	}
	logPath := info.Log
	if logPath == "" {
		logPath = lay.LogPath
	}
	if err := requestStop(s); err != nil {
		note("upgrade failed: " + err.Error())
		return err
	}
	note("copying " + from + " to " + dest)
	// Stopped above. Replace kite.exe and kite-agent.exe together. A failure
	// puts the previous files back and starts the old binary.
	var pairs [][2]string
	kiteSrc := filepath.Join(filepath.Dir(from), "kite.exe")
	kiteDst := filepath.Join(filepath.Dir(dest), "kite.exe")
	if st, statErr := os.Stat(kiteSrc); statErr == nil && !st.IsDir() && !pathsEqual(kiteSrc, kiteDst) {
		pairs = append(pairs, [2]string{kiteSrc, kiteDst})
	}
	if !pathsEqual(from, dest) {
		pairs = append(pairs, [2]string{from, dest})
	}
	cleanup, err := replaceFiles(pairs)
	if err != nil {
		note("upgrade failed: " + err.Error())
		_ = startAndWait(s, logPath)
		return err
	}
	cleanup()
	note("starting " + name)
	if err := startAndWait(s, logPath); err != nil {
		note("upgrade failed: " + err.Error())
		return err
	}
	note("upgrade ok: " + name + " running " + dest)
	return nil
}

func absFile(path string) (string, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = abs
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("new binary: %w", err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("new binary %s is a directory", path)
	}
	return path, nil
}

var errUpgradeBusy = fmt.Errorf("upgrade already running")

func takeUpgradeLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		st, statErr := os.Stat(path)
		if statErr == nil && time.Since(st.ModTime()) < 30*time.Second {
			return nil, errUpgradeBusy
		}
		_ = os.Remove(path)
	}
	return nil, errUpgradeBusy
}

func appendUpgradeLog(path, msg string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), msg)
}

func schtasks(args ...string) error {
	cmd := exec.Command("schtasks.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

func writeUTF16File(path, content string) error {
	content = strings.Replace(content, `encoding="UTF-8"`, `encoding="UTF-16"`, 1)
	encoded := utf16.Encode([]rune(content))
	buf := make([]byte, 2+len(encoded)*2)
	buf[0] = 0xFF
	buf[1] = 0xFE
	for i, u := range encoded {
		buf[2+i*2] = byte(u)
		buf[3+i*2] = byte(u >> 8)
	}
	return os.WriteFile(path, buf, 0o600)
}
