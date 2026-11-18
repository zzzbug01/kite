//go:build windows

package agentsvc

import (
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
	return FormatList(rows), nil
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
