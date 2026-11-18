package agentsvc

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
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

// settingsSchemaOrder is the settingsType sequence from the Task Scheduler
// 1.2 schema. schtasks rejects documents whose settings elements are out of
// that order, and it rejects LogonType ServiceAccount outright on Windows 11.
var settingsSchemaOrder = []string{
	"AllowStartOnDemand",
	"RestartOnFailure",
	"MultipleInstancesPolicy",
	"DisallowStartIfOnBatteries",
	"StopIfGoingOnBatteries",
	"AllowHardTerminate",
	"StartWhenAvailable",
	"RunOnlyIfNetworkAvailable",
	"WakeToRun",
	"Enabled",
	"Hidden",
	"DeleteExpiredTaskAfter",
	"Priority",
	"RunOnlyIfIdle",
	"ExecutionTimeLimit",
}

// UpgradeTaskXML is a Task Scheduler document with no trigger. schtasks /Run
// starts it. LOCAL SYSTEM is selected by UserId S-1-5-18 alone. LogonType is
// omitted: Windows 11 schtasks rejects <LogonType>ServiceAccount</LogonType>
// as "incorrectly formatted or out of range".
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
		ID       string `xml:"id,attr"`
		UserID   string `xml:"UserId"`
		RunLevel string `xml:"RunLevel"`
	}
	// Field order follows the 1.2 settingsType sequence.
	type settings struct {
		AllowStartOnDemand         bool   `xml:"AllowStartOnDemand"`
		MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
		DisallowStartIfOnBatteries bool   `xml:"DisallowStartIfOnBatteries"`
		StopIfGoingOnBatteries     bool   `xml:"StopIfGoingOnBatteries"`
		AllowHardTerminate         bool   `xml:"AllowHardTerminate"`
		StartWhenAvailable         bool   `xml:"StartWhenAvailable"`
		Enabled                    bool   `xml:"Enabled"`
		Hidden                     bool   `xml:"Hidden"`
		ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
	}
	doc := struct {
		XMLName     xml.Name `xml:"Task"`
		Version     string   `xml:"version,attr"`
		Xmlns       string   `xml:"xmlns,attr"`
		Description string   `xml:"RegistrationInfo>Description"`
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
			AllowStartOnDemand:         true,
			MultipleInstancesPolicy:    "IgnoreNew",
			DisallowStartIfOnBatteries: false,
			StopIfGoingOnBatteries:     false,
			AllowHardTerminate:         true,
			StartWhenAvailable:         true,
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
		ID:       "Author",
		UserID:   "S-1-5-18",
		RunLevel: "HighestAvailable",
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	// The in-memory document is UTF-8. writeUTF16File switches the declaration
	// when it encodes the file schtasks reads.
	text := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" + string(body) + "\n"
	if err := validateUpgradeTaskXML(text); err != nil {
		return "", err
	}
	return text, nil
}

// validateUpgradeTaskXML checks the document against the constraints schtasks
// enforces on Windows 11. It cannot call schtasks itself.
func validateUpgradeTaskXML(xmlText string) error {
	if strings.Contains(xmlText, "LogonType") || strings.Contains(xmlText, "ServiceAccount") {
		return errors.New("LogonType ServiceAccount is rejected by schtasks on Windows 11; select SYSTEM with UserId S-1-5-18 only")
	}
	if strings.Contains(xmlText, "AllowDemandStart") {
		return errors.New("AllowDemandStart is not a Task Scheduler 1.2 element; use AllowStartOnDemand")
	}
	dec := xml.NewDecoder(strings.NewReader(xmlText))
	var (
		userID, runLevel, command, arguments string
		settings                             []string
		inSettings                           bool
		depth                                int
		settingsDepth                        int
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("task xml: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			depth++
			switch el.Name.Local {
			case "Settings":
				inSettings = true
				settingsDepth = depth
			case "UserId":
				if err := dec.DecodeElement(&userID, &el); err != nil {
					return err
				}
				depth--
			case "RunLevel":
				if err := dec.DecodeElement(&runLevel, &el); err != nil {
					return err
				}
				depth--
			case "Command":
				if err := dec.DecodeElement(&command, &el); err != nil {
					return err
				}
				depth--
			case "Arguments":
				if err := dec.DecodeElement(&arguments, &el); err != nil {
					return err
				}
				depth--
			default:
				if inSettings && depth == settingsDepth+1 {
					settings = append(settings, el.Name.Local)
				}
			}
		case xml.EndElement:
			if el.Name.Local == "Settings" {
				inSettings = false
			}
			depth--
		}
	}
	if userID != "S-1-5-18" {
		return fmt.Errorf("principal UserId = %q, want S-1-5-18 (LOCAL SYSTEM)", userID)
	}
	if runLevel != "HighestAvailable" {
		return fmt.Errorf("principal RunLevel = %q, want HighestAvailable", runLevel)
	}
	if strings.TrimSpace(command) == "" {
		return errors.New("task Exec Command is empty")
	}
	if !strings.Contains(arguments, "upgrade --apply") || !strings.Contains(arguments, "--from") || !strings.Contains(arguments, "--service-name") {
		return fmt.Errorf("task arguments %q are missing the upgrade worker flags", arguments)
	}
	if strings.Contains(strings.ToLower(xmlText), "registration_key") {
		return errors.New("task xml contains a registration key")
	}
	prev := -1
	sawDemand := false
	for _, name := range settings {
		if name == "AllowStartOnDemand" {
			sawDemand = true
		}
		idx := -1
		for i, schemaName := range settingsSchemaOrder {
			if schemaName == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("settings element %q is not in the Task Scheduler 1.2 schema", name)
		}
		if idx < prev {
			return fmt.Errorf("settings element %q is out of schema order", name)
		}
		prev = idx
	}
	if !sawDemand {
		return errors.New("settings missing AllowStartOnDemand")
	}
	return nil
}
