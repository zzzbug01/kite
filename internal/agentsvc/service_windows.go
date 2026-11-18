//go:build windows

package agentsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/zzzbug01/kite/internal/config"
)

const (
	displayName = "Kite Agent"
	description = "Dial-out SSH tunnel to a kite relay. Runs in the background with no console window."
	stopWait    = 20 * time.Second
	startWait   = 20 * time.Second
)

// RunningAsService reports whether the Service Control Manager started this
// process. Services run in session 0 and do not get a console window.
func RunningAsService() (bool, error) {
	return svc.IsWindowsService()
}

type handler struct {
	run func(context.Context) error
}

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- h.run(ctx)
	}()
	changes <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				return true, 1
			}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				stopPending := svc.Status{State: svc.StopPending, WaitHint: 25000}
				changes <- stopPending
				cancel()
				timer := time.NewTimer(stopWait)
				for {
					select {
					case err := <-done:
						timer.Stop()
						if err != nil && !errors.Is(err, context.Canceled) {
							return true, 1
						}
						return false, 0
					case c := <-r:
						if c.Cmd == svc.Interrogate {
							changes <- stopPending
						}
					case <-timer.C:
						return true, 1
					}
				}
			}
		}
	}
}

// Serve runs fn as the service handler until the SCM asks the process to stop.
func Serve(fn func(context.Context) error) error {
	elog := openEventLog()
	if elog != nil {
		_ = elog.Info(1, "kite-agent service starting")
		defer elog.Close()
	}
	err := svc.Run(ServiceName, &handler{run: fn})
	if err != nil {
		if elog != nil {
			_ = elog.Error(1, "kite-agent service failed: "+clip(err.Error()))
		}
		return err
	}
	if elog != nil {
		_ = elog.Info(1, "kite-agent service stopped")
	}
	return nil
}

// LogServiceError writes a configuration or startup error to the Application
// event log. The service has no console, so this is the record Event Viewer shows.
func LogServiceError(err error) {
	if err == nil {
		return
	}
	elog := openEventLog()
	if elog == nil {
		return
	}
	defer elog.Close()
	_ = elog.Error(1, clip(err.Error()))
}

func openEventLog() *eventlog.Log {
	elog, err := eventlog.Open(ServiceName)
	if err != nil {
		return nil
	}
	return elog
}

func clip(s string) string {
	if len(s) > 30000 {
		return s[:30000]
	}
	return s
}

// Install writes the service config, registers kite-agent, and starts it.
func Install(opt Options) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	if err := rejectPlaceholderRelay(opt.Config.RelayAddr); err != nil {
		return err
	}
	configPath, err := serviceConfigPath(opt.ConfigPath)
	if err != nil {
		return err
	}
	if err := WriteAgentFile(configPath, opt.Config); err != nil {
		return err
	}
	exe, err := currentExe()
	if err != nil {
		return err
	}
	installDir := opt.InstallDir
	if strings.TrimSpace(installDir) == "" {
		installDir = defaultInstallDir()
	}
	dest := filepath.Join(installDir, "kite-agent.exe")

	m, err := connectSCM(windows.SC_MANAGER_ALL_ACCESS)
	if err != nil {
		return err
	}
	defer m.Disconnect()

	existed := true
	s, err := openService(m, windows.SERVICE_ALL_ACCESS)
	if err != nil {
		if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("open service: %w", err)
		}
		existed = false
		s = nil
	}
	if s != nil {
		defer s.Close()
		if err := requestStop(s); err != nil {
			return fmt.Errorf("stop existing service before replacing the binary: %w", err)
		}
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return err
	}
	if !samePath(exe, dest) {
		if err := copyFileRetry(dest, exe); err != nil {
			if existed {
				return fmt.Errorf("copy %s to %s: %w; the existing service was stopped and was not started again", exe, dest, err)
			}
			return fmt.Errorf("copy %s to %s: %w", exe, dest, err)
		}
	}
	copyOptionalSibling(exe, installDir, "kite.exe")
	removedTask := removeLegacyLogonTask()
	removedWrapper := false
	if err := os.Remove(filepath.Join(installDir, "start-kite-agent.cmd")); err == nil {
		removedWrapper = true
	}

	if s == nil {
		created, err := m.CreateService(ServiceName, dest, serviceConfig(), "--config", configPath)
		if err != nil {
			if errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
				return fmt.Errorf("service %s is marked for deletion; wait a moment and run kite-agent install again", ServiceName)
			}
			return fmt.Errorf("create service: %w", err)
		}
		s = created
		defer s.Close()
	} else if err := s.UpdateConfig(serviceConfigWithPath(windowsCommandLine(dest, configPath))); err != nil {
		return fmt.Errorf("update service: %w", err)
	}
	warnRecovery(s)
	installEventSource()

	if err := startAndWait(s); err != nil {
		return fmt.Errorf("service %s is installed but did not stay running: %w\nLog: %s", ServiceName, err, filepath.Join(filepath.Dir(configPath), logFileName))
	}

	fmt.Printf("Installed service %s.\n", ServiceName)
	fmt.Printf("Binary: %s\n", dest)
	fmt.Printf("Config: %s\n", configPath)
	fmt.Printf("Log: %s\n", filepath.Join(filepath.Dir(configPath), logFileName))
	fmt.Printf("Start type: automatic (delayed)\n")
	fmt.Printf("Account: LocalSystem\n")
	fmt.Printf("State: running\n")
	fmt.Printf("The service starts at boot, without a logged-on user, and does not open a console window.\n")
	if !existed {
		fmt.Printf("Created a new service.\n")
	}
	if removedTask {
		fmt.Printf("Removed the legacy logon scheduled task %q.\n", ServiceName)
	}
	if removedWrapper {
		fmt.Printf("Removed the legacy start-kite-agent.cmd wrapper.\n")
	}
	return nil
}

// Uninstall removes the service and leaves the config file on disk.
func Uninstall() error {
	if err := requireAdmin(); err != nil {
		return err
	}
	m, err := connectSCM(windows.SC_MANAGER_ALL_ACCESS)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := openService(m, windows.SERVICE_ALL_ACCESS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			fmt.Printf("Service %s is not installed.\n", ServiceName)
			return nil
		}
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	info, _ := readStatus(s)
	if err := requestStop(s); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	_ = eventlog.Remove(ServiceName)
	fmt.Printf("Removed Windows service %s.\n", ServiceName)
	cfg := info.Config
	if cfg == "" {
		cfg = config.WindowsMachineConfig()
	}
	fmt.Printf("The registration key was left on disk. Delete this file to remove it:\n  %s\n", cfg)
	if info.Binary != "" {
		fmt.Printf("Left the binary in place: %s\n", info.Binary)
	}
	return nil
}

// Start starts the service.
func Start() error {
	return withMutatingService(func(s *mgr.Service) error {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Running {
			fmt.Printf("%s is already running.\n", ServiceName)
			return printStatus(s)
		}
		if err := startAndWait(s); err != nil {
			return err
		}
		fmt.Printf("Started %s.\n", ServiceName)
		return printStatus(s)
	})
}

// Stop stops the service.
func Stop() error {
	return withMutatingService(func(s *mgr.Service) error {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			fmt.Printf("%s is already stopped.\n", ServiceName)
			return nil
		}
		if err := requestStop(s); err != nil {
			return err
		}
		fmt.Printf("Stopped %s.\n", ServiceName)
		return nil
	})
}

// Restart stops the service if it is running, then starts it.
func Restart() error {
	return withMutatingService(func(s *mgr.Service) error {
		if err := requestStop(s); err != nil {
			return err
		}
		if err := startAndWait(s); err != nil {
			return err
		}
		fmt.Printf("Restarted %s.\n", ServiceName)
		return printStatus(s)
	})
}

// Status reports the service state. It does not need an elevated prompt.
func Status() (string, error) {
	m, err := connectSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	s, err := openService(m, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		s, err = openService(m, windows.SERVICE_QUERY_STATUS)
	}
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return FormatStatus(StatusInfo{}), nil
		}
		return "", fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	info, err := readStatus(s)
	if err != nil {
		return "", err
	}
	return FormatStatus(info), nil
}

func printStatus(s *mgr.Service) error {
	info, err := readStatus(s)
	if err != nil {
		return err
	}
	fmt.Print(FormatStatus(info))
	return nil
}

func withMutatingService(fn func(*mgr.Service) error) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	m, err := connectSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	access := uint32(windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG | windows.SERVICE_START | windows.SERVICE_STOP)
	s, err := openService(m, access)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("service %s is not installed; run kite-agent install from an elevated prompt", ServiceName)
		}
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	return fn(s)
}

func readStatus(s *mgr.Service) (StatusInfo, error) {
	st, err := s.Query()
	if err != nil {
		return StatusInfo{}, err
	}
	info := StatusInfo{
		Installed: true,
		State:     stateName(st.State),
		PID:       st.ProcessId,
		Log:       DefaultLogPath(),
		Config:    config.WindowsMachineConfig(),
	}
	if st.State == svc.Stopped && st.Win32ExitCode != 0 && st.Win32ExitCode != uint32(windows.ERROR_SERVICE_NEVER_STARTED) {
		info.ExitCode = st.Win32ExitCode
	}
	cfg, err := s.Config()
	if err != nil {
		return info, nil
	}
	info.StartType = startTypeName(cfg)
	info.Account = accountName(cfg.ServiceStartName)
	info.Binary, info.Config = parseServiceCommand(cfg.BinaryPathName)
	if info.Config == "" {
		info.Config = config.WindowsMachineConfig()
	}
	if info.Config != "" {
		info.Log = filepath.Join(filepath.Dir(info.Config), logFileName)
	}
	return info, nil
}

func serviceConfig() mgr.Config {
	return mgr.Config{
		DisplayName:      displayName,
		Description:      description,
		StartType:        mgr.StartAutomatic,
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		ErrorControl:     mgr.ErrorNormal,
		DelayedAutoStart: true,
	}
}

func serviceConfigWithPath(binary string) mgr.Config {
	cfg := serviceConfig()
	cfg.BinaryPathName = binary
	return cfg
}

func windowsCommandLine(exe, configPath string) string {
	return syscall.EscapeArg(exe) + " " + syscall.EscapeArg("--config") + " " + syscall.EscapeArg(configPath)
}

func serviceConfigPath(explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit == "" {
		p := config.WindowsMachineConfig()
		if p == "" {
			return "", errors.New("no Windows config path")
		}
		return p, nil
	}
	if !filepath.IsAbs(explicit) {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		explicit = abs
	}
	if pathInUserProfile(explicit) {
		return "", fmt.Errorf("--config %s is inside a user profile; the service runs as LocalSystem and does not load that profile. Omit --config to write %%ProgramData%%\\kite\\config.yaml, or pass a path outside the profile", explicit)
	}
	return explicit, nil
}

func defaultInstallDir() string {
	root := strings.TrimSpace(os.Getenv("ProgramFiles"))
	if root == "" {
		root = `C:\Program Files`
	}
	return filepath.Join(root, "kite")
}

func currentExe() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Abs(p)
}

func requireAdmin() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return nil
	}
	return errors.New("administrator rights are required; open PowerShell or Command Prompt with Run as administrator")
}

func connectSCM(access uint32) (*mgr.Mgr, error) {
	var host *uint16
	h, err := windows.OpenSCManager(host, nil, access)
	if err != nil {
		return nil, fmt.Errorf("open service control manager: %w", elevate(err))
	}
	return &mgr.Mgr{Handle: h}, nil
}

func openService(m *mgr.Mgr, access uint32) (*mgr.Service, error) {
	name, err := syscall.UTF16PtrFromString(ServiceName)
	if err != nil {
		return nil, err
	}
	h, err := windows.OpenService(m.Handle, name, access)
	if err != nil {
		return nil, elevate(err)
	}
	return &mgr.Service{Name: ServiceName, Handle: h}, nil
}

func elevate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("%w (run this from an elevated Administrator prompt)", err)
	}
	return err
}

func requestStop(s *mgr.Service) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		switch st.State {
		case svc.Stopped:
			return nil
		case svc.StopPending:
			return waitFor(s, svc.Stopped, stopWait)
		}
		_, err = s.Control(svc.Stop)
		if err == nil || errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return waitFor(s, svc.Stopped, stopWait)
		}
		if time.Now().After(deadline) || !errors.Is(err, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func startAndWait(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.StopPending {
		if err := waitFor(s, svc.Stopped, stopWait); err != nil {
			return err
		}
		st.State = svc.Stopped
	}
	if st.State == svc.Stopped {
		if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return err
		}
	}
	deadline := time.Now().Add(startWait)
	for {
		st, err = s.Query()
		if err != nil {
			return err
		}
		switch st.State {
		case svc.Running:
			return nil
		case svc.Stopped:
			if st.Win32ExitCode != 0 {
				return fmt.Errorf("service stopped (exit %d); see %s", st.Win32ExitCode, DefaultLogPath())
			}
			return fmt.Errorf("service stopped; see %s", DefaultLogPath())
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting to run (state %s)", stateName(st.State))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitFor(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s (state %s)", stateName(want), stateName(st.State))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func warnRecovery(s *mgr.Service) {
	err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
	}, 24*60*60)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kite-agent: service registered, but crash recovery was not set: %s\n", err)
		return
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		fmt.Fprintf(os.Stderr, "kite-agent: service registered, but non-crash recovery was not set: %s\n", err)
	}
}

func installEventSource() {
	err := eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		fmt.Fprintf(os.Stderr, "kite-agent: event log source not installed: %s\n", err)
	}
}

func removeLegacyLogonTask() bool {
	cmd := exec.Command("schtasks.exe", "/Delete", "/TN", ServiceName, "/F")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run() == nil
}

func copyOptionalSibling(srcExe, destDir, name string) {
	src := filepath.Join(filepath.Dir(srcExe), name)
	if _, err := os.Stat(src); err != nil {
		return
	}
	dst := filepath.Join(destDir, name)
	if samePath(src, dst) {
		return
	}
	if err := copyFileRetry(dst, src); err != nil {
		fmt.Fprintf(os.Stderr, "kite-agent: left %s uncopied: %s\n", name, err)
	}
}

func copyFileRetry(dst, src string) error {
	var err error
	for i := 0; i < 20; i++ {
		err = copyFile(dst, src)
		if err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return err
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	closeErr := out.Close()
	if cerr != nil {
		os.Remove(tmp)
		return cerr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	if err := replaceFile(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	if strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb)) {
		return true
	}
	ia, err1 := os.Stat(aa)
	ib, err2 := os.Stat(bb)
	if err1 != nil || err2 != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

func stateName(st svc.State) string {
	switch st {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start pending"
	case svc.StopPending:
		return "stop pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue pending"
	case svc.PausePending:
		return "pause pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state %d", uint32(st))
	}
}

func startTypeName(c mgr.Config) string {
	switch c.StartType {
	case mgr.StartAutomatic:
		if c.DelayedAutoStart {
			return "automatic (delayed)"
		}
		return "automatic"
	case mgr.StartManual:
		return "manual"
	case mgr.StartDisabled:
		return "disabled"
	default:
		return fmt.Sprintf("other (%d)", c.StartType)
	}
}

func accountName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "LocalSystem") || strings.EqualFold(name, `NT AUTHORITY\SYSTEM`) {
		return "LocalSystem"
	}
	return name
}
