package agentsvc

import (
	"encoding/xml"
	"path/filepath"
	"strings"
)

// UpgradeTaskName is the one-shot scheduled task that performs a binary swap.
// It is not the legacy logon task, and it is distinct per service instance.
func UpgradeTaskName(serviceName string) string {
	return "kite-upgrade-" + serviceName
}

// UpgradeResultFile is the log the detached upgrade writes. It sits next to
// that instance's config and does not contain the registration key.
func UpgradeResultFile(lay Layout) string {
	return filepath.Join(filepath.Dir(lay.ConfigPath), "upgrade.log")
}

// quoteWindowsArg quotes one CreateProcess argument the way Windows expects.
func quoteWindowsArg(s string) string {
	if len(s) == 0 {
		return `""`
	}
	needsQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\\', ' ', '\t':
			needsQuote = true
		}
	}
	if !needsQuote {
		return s
	}
	var b strings.Builder
	hasSpace := strings.ContainsAny(s, " \t")
	needsBackslash := strings.ContainsAny(s, `"\`)
	if !needsBackslash {
		b.WriteByte('"')
		b.WriteString(s)
		b.WriteByte('"')
		return b.String()
	}
	if hasSpace {
		b.WriteByte('"')
	}
	slashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		default:
			slashes = 0
		case '\\':
			slashes++
		case '"':
			for ; slashes > 0; slashes-- {
				b.WriteByte('\\')
			}
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	if hasSpace {
		for ; slashes > 0; slashes-- {
			b.WriteByte('\\')
		}
		b.WriteByte('"')
	}
	return b.String()
}

// UpgradeArguments is the argument string the scheduled task passes to the new exe.
func UpgradeArguments(from, serviceName string) string {
	return "upgrade --apply --from " + quoteWindowsArg(from) + " --service-name " + quoteWindowsArg(serviceName)
}

// UpgradeTaskXML is a Task Scheduler document with no trigger. The caller
// starts it on demand. The principal is LOCAL SYSTEM, so the swap survives
// the interactive session that launched it.
func UpgradeTaskXML(exe, from, serviceName string) (string, error) {
	canon, err := CanonicalServiceName(serviceName)
	if err != nil {
		return "", err
	}
	type exec struct {
		Command   string `xml:"Command"`
		Arguments string `xml:"Arguments"`
	}
	type actions struct {
		Context string `xml:"Context,attr"`
		Exec    exec   `xml:"Exec"`
	}
	type principal struct {
		ID        string `xml:"id,attr"`
		UserID    string `xml:"UserId"`
		LogonType string `xml:"LogonType"`
		RunLevel  string `xml:"RunLevel"`
	}
	type settings struct {
		MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
		DisallowStartIfOnBatteries bool   `xml:"DisallowStartIfOnBatteries"`
		StopIfGoingOnBatteries     bool   `xml:"StopIfGoingOnBatteries"`
		AllowHardTerminate         bool   `xml:"AllowHardTerminate"`
		StartWhenAvailable         bool   `xml:"StartWhenAvailable"`
		AllowDemandStart           bool   `xml:"AllowDemandStart"`
		Enabled                    bool   `xml:"Enabled"`
		Hidden                     bool   `xml:"Hidden"`
		ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
	}
	doc := struct {
		XMLName     xml.Name `xml:"Task"`
		Version     string   `xml:"version,attr"`
		Xmlns       string   `xml:"xmlns,attr"`
		Description string   `xml:"RegistrationInfo>Description"`
		Triggers    struct{} `xml:"Triggers"`
		Principals  struct {
			Principal principal `xml:"Principal"`
		} `xml:"Principals"`
		Settings settings `xml:"Settings"`
		Actions  actions  `xml:"Actions"`
	}{
		Version:     "1.2",
		Xmlns:       "http://schemas.microsoft.com/windows/2004/02/mit/task",
		Description: "One-shot kite-agent binary swap for " + canon + ". Contains no secrets.",
		Settings: settings{
			MultipleInstancesPolicy:    "IgnoreNew",
			DisallowStartIfOnBatteries: false,
			StopIfGoingOnBatteries:     false,
			AllowHardTerminate:         true,
			StartWhenAvailable:         true,
			AllowDemandStart:           true,
			Enabled:                    true,
			Hidden:                     true,
			ExecutionTimeLimit:         "PT10M",
		},
		Actions: actions{
			Context: "Author",
			Exec: exec{
				Command:   exe,
				Arguments: UpgradeArguments(from, canon),
			},
		},
	}
	doc.Principals.Principal = principal{
		ID:        "Author",
		UserID:    "S-1-5-18",
		LogonType: "ServiceAccount",
		RunLevel:  "HighestAvailable",
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return "<?xml version=\"1.0\" encoding=\"UTF-16\"?>\n" + string(body) + "\n", nil
}
