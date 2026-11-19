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
// name is the service the SCM launched. The default is kite-agent.
func Serve(name string, fn func(context.Context) error) error {
	name, err := CanonicalServiceName(name)
	if err != nil {
		return err
	}
	elog := openEventLog(name)
	if elog != nil {
		_ = elog.Info(1, name+" service starting")
		defer elog.Close()
	}
	err = svc.Run(name, &handler{run: fn})
	if err != nil {
		if elog != nil {
			_ = elog.Error(1, name+" service failed: "+clip(err.Error()))
		}
		return err
	}
	if elog != nil {
		_ = elog.Info(1, name+" service stopped")
	}
	return nil
}

// LogServiceError writes a configuration or startup error to the Application
// event log. The service has no console, so this is the record Event Viewer shows.
func LogServiceError(name string, err error) {
	if err == nil {
		return
	}
	name, nerr := CanonicalServiceName(name)
	if nerr != nil {
		name = ServiceName
	}
	elog := openEventLog(name)
	if elog == nil {
		return
	}
	defer elog.Close()
	_ = elog.Error(1, clip(err.Error()))
}

func openEventLog(name string) *eventlog.Log {
	elog, err := eventlog.Open(name)
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

// Preflight reports whether this process can register the service.
func Preflight(Options) error {
	return requireAdmin()
}

// RequireUpgradePriv is the elevation check for a download-and-replace upgrade.
func RequireUpgradePriv() error {
	return requireAdmin()
}

// Install writes the service config, registers one instance, and starts it.
func Install(opt Options) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	name, err := CanonicalServiceName(opt.ServiceName)
	if err != nil {
		return err
	}
	if err := rejectPlaceholderRelay(opt.Config.RelayAddr); err != nil {
		return err
	}
	lay, err := DefaultLayout(name)
	if err != nil {
		return err
	}
	configPath, err := serviceConfigPath(opt.ConfigPath, lay)
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
	installDir := strings.TrimSpace(opt.InstallDir)
	if installDir == "" {
		installDir = lay.InstallDir
	}
	dest := filepath.Join(installDir, "kite-agent.exe")
	logPath := filepath.Join(filepath.Dir(configPath), logFileName)

	m, err := connectSCM(windows.SC_MANAGER_ALL_ACCESS)
	if err != nil {
		return err
	}
	defer m.Disconnect()

	existed := true
	s, err := openService(m, name, windows.SERVICE_ALL_ACCESS)
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
	removedTask := false
	removedWrapper := false
	if CleansLegacyTask(name) {
		removedTask = removeLegacyLogonTask()
		if err := os.Remove(filepath.Join(installDir, "start-kite-agent.cmd")); err == nil {
			removedWrapper = true
		}
	}

	args := ServiceArgs(configPath, name)
	binPath := windowsCommandLine(dest, args...)
	if s == nil {
		created, err := m.CreateService(name, dest, serviceConfig(name), args...)
		if err != nil {
			if errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
				return fmt.Errorf("service %s is marked for deletion; wait a moment and run %s again", name, instanceCommand("install", name))
			}
			return fmt.Errorf("create service: %w", err)
		}
		s = created
		defer s.Close()
	} else if err := s.UpdateConfig(serviceConfigWithPath(name, binPath)); err != nil {
		return fmt.Errorf("update service: %w", err)
	}
	warnRecovery(s)
	installEventSource(name)

	if err := startAndWait(s, logPath); err != nil {
		return fmt.Errorf("service %s is installed but did not stay running: %w\nLog: %s", name, err, logPath)
	}

	fmt.Printf("Installed service %s.\n", name)
	fmt.Printf("Binary: %s\n", dest)
	fmt.Printf("Config: %s\n", configPath)
	fmt.Printf("Log: %s\n", logPath)
	fmt.Printf("Start type: automatic (delayed)\n")
	fmt.Printf("Account: LocalSystem\n")
	fmt.Printf("State: running\n")
	fmt.Printf("The service starts at boot, without a logged-on user, and does not open a console window.\n")
	if name != ServiceName {
		fmt.Printf("Left service %s unchanged.\n", ServiceName)
	}
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

// Uninstall removes one service and leaves its config file on disk.
func Uninstall(name string) error {
	name, err := CanonicalServiceName(name)
	if err != nil {
		return err
	}
	if err := requireAdmin(); err != nil {
		return err
	}
	m, err := connectSCM(windows.SC_MANAGER_ALL_ACCESS)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := openService(m, name, windows.SERVICE_ALL_ACCESS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			fmt.Printf("Service %s is not installed.\n", name)
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
	_ = eventlog.Remove(name)
	fmt.Printf("Removed Windows service %s.\n", name)
	cfg := info.Config
	if cfg == "" {
		if lay, lerr := DefaultLayout(name); lerr == nil {
			cfg = lay.ConfigPath
		}
	}
	fmt.Printf("The config was left on disk (it may contain a registration key or an agent token). Delete this file to remove it:\n  %s\n", cfg)
	if info.Binary != "" {
		fmt.Printf("Left the binary in place: %s\n", info.Binary)
	}
	return nil
}

// Start starts one service.
func Start(name string) error {
	return withMutatingService(name, func(s *mgr.Service) error {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Running {
			fmt.Printf("%s is already running.\n", s.Name)
			return printStatus(s)
		}
		if err := startAndWait(s, logPathFor(s)); err != nil {
			return err
		}
		fmt.Printf("Started %s.\n", s.Name)
		return printStatus(s)
	})
}

// Stop stops one service.
func Stop(name string) error {
	return withMutatingService(name, func(s *mgr.Service) error {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			fmt.Printf("%s is already stopped.\n", s.Name)
			return nil
		}
		if err := requestStop(s); err != nil {
			return err
		}
		fmt.Printf("Stopped %s.\n", s.Name)
		return nil
	})
}

// Restart stops one service if it is running, then starts it.
func Restart(name string) error {
	return withMutatingService(name, func(s *mgr.Service) error {
		if err := requestStop(s); err != nil {
			return err
		}
		if err := startAndWait(s, logPathFor(s)); err != nil {
			return err
		}
		fmt.Printf("Restarted %s.\n", s.Name)
		return printStatus(s)
	})
}

// Status reports one service. It does not need an elevated prompt.
func Status(name string) (string, error) {
	name, err := CanonicalServiceName(name)
	if err != nil {
		return "", err
	}
	m, err := connectSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	s, err := openService(m, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		s, err = openService(m, name, windows.SERVICE_QUERY_STATUS)
	}
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return FormatStatus(StatusInfo{Name: name}), nil
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

func withMutatingService(name string, fn func(*mgr.Service) error) error {
	name, err := CanonicalServiceName(name)
	if err != nil {
		return err
	}
	if err := requireAdmin(); err != nil {
		return err
	}
	m, err := connectSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	access := uint32(windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG | windows.SERVICE_START | windows.SERVICE_STOP)
	s, err := openService(m, name, access)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("service %s is not installed; run %s from an elevated prompt", name, instanceCommand("install", name))
		}
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	return fn(s)
}

func logPathFor(s *mgr.Service) string {
	info, err := readStatus(s)
	if err == nil && info.Log != "" {
		return info.Log
	}
	lay, err := DefaultLayout(s.Name)
	if err != nil {
		return ""
	}
	return lay.LogPath
}

func readStatus(s *mgr.Service) (StatusInfo, error) {
	st, err := s.Query()
	if err != nil {
		return StatusInfo{}, err
	}
	lay, _ := DefaultLayout(s.Name)
	info := StatusInfo{
		Name:      s.Name,
		Installed: true,
		State:     stateName(st.State),
		PID:       st.ProcessId,
		Log:       lay.LogPath,
		Config:    lay.ConfigPath,
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
		info.Config = lay.ConfigPath
	}
	if info.Config != "" {
		info.Log = filepath.Join(filepath.Dir(info.Config), logFileName)
	}
	return info, nil
}

func serviceConfig(name string) mgr.Config {
	disp := displayName
	if name != ServiceName {
		disp = displayName + " (" + name + ")"
	}
	return mgr.Config{
		DisplayName:      disp,
		Description:      description,
		StartType:        mgr.StartAutomatic,
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		ErrorControl:     mgr.ErrorNormal,
		DelayedAutoStart: true,
	}
}

func serviceConfigWithPath(name, binary string) mgr.Config {
	cfg := serviceConfig(name)
	cfg.BinaryPathName = binary
	return cfg
}

func windowsCommandLine(exe string, args ...string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, syscall.EscapeArg(exe))
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

func serviceConfigPath(explicit string, lay Layout) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit == "" {
		if lay.ConfigPath == "" {
			return "", errors.New("no Windows config path")
		}
		return lay.ConfigPath, nil
	}
	if !filepath.IsAbs(explicit) {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		explicit = abs
	}
	if pathInUserProfile(explicit) {
		return "", fmt.Errorf("--config %s is inside a user profile; the service runs as LocalSystem and does not load that profile. Omit --config to write %s, or pass a path outside the profile", explicit, lay.ConfigPath)
	}
	return explicit, nil
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

func openService(m *mgr.Mgr, name string, access uint32) (*mgr.Service, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.OpenService(m.Handle, namePtr, access)
	if err != nil {
		return nil, elevate(err)
	}
	return &mgr.Service{Name: name, Handle: h}, nil
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

func startAndWait(s *mgr.Service, logPath string) error {
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
				return fmt.Errorf("service stopped (exit %d); see %s", st.Win32ExitCode, logPath)
			}
			return fmt.Errorf("service stopped; see %s", logPath)
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

func installEventSource(name string) {
	err := eventlog.InstallAsEventCreate(name, eventlog.Error|eventlog.Warning|eventlog.Info)
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
