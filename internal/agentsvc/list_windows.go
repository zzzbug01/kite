//go:build windows

package agentsvc

import (
	"bytes"
	"encoding/csv"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// List prints every installed service named kite-agent or kite-agent-*.
// Query access is enough; an elevated prompt is not required.
func List() (string, error) {
	m, err := connectSCM(windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	names, err := m.ListServices()
	if err != nil {
		return "", elevate(err)
	}
	var rows []ListedService
	for _, name := range names {
		if !IsKiteAgentService(name) {
			continue
		}
		rows = append(rows, describeService(m, name))
	}
	text := FormatList(rows)
	if status, ok := legacyLogonTaskStatus(); ok {
		text += FormatLegacyLogonTask(status)
	}
	return text, nil
}

// legacyLogonTaskStatus reads the old logon task named kite-agent, if it
// still exists. The query does not change the task. A missing task or a
// query error leaves list's service rows intact.
func legacyLogonTaskStatus() (string, bool) {
	cmd := exec.Command("schtasks.exe", "/Query", "/TN", ServiceName, "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	text := string(bytes.TrimPrefix(out, []byte{0xEF, 0xBB, 0xBF}))
	rec, err := csv.NewReader(strings.NewReader(text)).Read()
	if err != nil || len(rec) < 3 {
		return "", false
	}
	status := strings.TrimSpace(rec[2])
	if status == "" {
		return "", false
	}
	return status, true
}

func describeService(m *mgr.Mgr, name string) ListedService {
	row := ListedService{Name: name, AgentName: "-", State: "-", StartType: "-"}
	s, err := openService(m, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		s, err = openService(m, name, windows.SERVICE_QUERY_STATUS)
	}
	if err != nil {
		return row
	}
	defer s.Close()
	info, err := readStatus(s)
	if err != nil {
		return row
	}
	if info.State != "" {
		row.State = info.State
	}
	if info.StartType != "" {
		row.StartType = info.StartType
	}
	row.AgentName = agentNameFromConfig(info.Config)
	return row
}
