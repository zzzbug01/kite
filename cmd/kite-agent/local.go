package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/agent"
	"github.com/zzzbug01/kite/internal/agentctl"
	"github.com/zzzbug01/kite/internal/agentsvc"
	"github.com/zzzbug01/kite/internal/authkeys"
	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/pairing"
	"github.com/zzzbug01/kite/internal/sshaddr"
	"github.com/zzzbug01/kite/internal/sshsetup"
)

func runSetup(args []string) error {
	fs := flag.NewFlagSet("kite-agent setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	local := fs.String("local", "127.0.0.1:22", "local SSH address the agent dials (env LOCAL_SSH_ADDR)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite-agent setup [--local 127.0.0.1:22]\n\n")
		fmt.Fprintf(fs.Output(), "setup checks the local SSH server. When this process is root (or an elevated\n")
		fmt.Fprintf(fs.Output(), "Windows prompt), a missing server is installed and sshd is set to start\n")
		fmt.Fprintf(fs.Output(), "automatically. Otherwise the exact install command is printed.\n")
		fmt.Fprintf(fs.Output(), "No firewall rule is added. kite-agent dials 127.0.0.1 only.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if v := os.Getenv("LOCAL_SSH_ADDR"); v != "" && *local == "127.0.0.1:22" {
		*local = v
	}
	rep, err := sshsetup.Ensure(context.Background(), *local)
	fmt.Fprint(os.Stdout, rep.Text())
	out := sshaddr.Detect(context.Background())
	if out.Detail != "" {
		fmt.Fprintln(os.Stdout, out.Detail)
	}
	if out.Confident {
		fmt.Fprintf(os.Stdout, "local ssh: %s (auto)\n", out.Addr)
	} else {
		fmt.Fprintf(os.Stdout, "local ssh: detection inconclusive; not assuming %s\n", out.Addr)
	}
	return err
}

func runPair(args []string) error {
	fs := flag.NewFlagSet("kite-agent pair", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := config.PeekConfigFlag(args)
	ttl := fs.Duration("ttl", 10*time.Minute, "how long the pairing code stays valid")
	var serviceName string
	fs.StringVar(&configPath, "config", configPath, "agent config file (or KITE_CONFIG)")
	fs.StringVar(&serviceName, "service-name", "", "instance whose control file to use (default kite-agent)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite-agent pair [--ttl 10m] [--service-name name]\n\n")
		fmt.Fprintf(fs.Output(), "pair asks the relay for a one-time pairing code and prints it.\n")
		fmt.Fprintf(fs.Output(), "The code is not the registration key. It expires and works once.\n")
		fmt.Fprintf(fs.Output(), "On the computer you connect from: kite connect CODE\n\n")
		fmt.Fprintf(fs.Output(), "pair talks to the running agent over 127.0.0.1 when the control\n")
		fmt.Fprintf(fs.Output(), "file is readable, so a second elevation prompt is not required.\n")
		fmt.Fprintf(fs.Output(), "If the service is not reachable, pair uses the registration key or\n")
		fmt.Fprintf(fs.Output(), "agent token from the environment or the config file. The agent must\n")
		fmt.Fprintf(fs.Output(), "be connected. An agent installed with --code pairs with its token.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *ttl < time.Minute || *ttl > 30*time.Minute {
		return errors.New("ttl must be between 1 minute and 30 minutes")
	}
	name, err := agentsvc.CanonicalServiceName(serviceName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var last, primaryErr error
	sshUser := defaultSSHUser()
	instCfg := instanceConfig(configPath, name)
	if sshUser != "" && instCfg != "" {
		_ = config.SetSSHUser(instCfg, sshUser)
	}
	paths := agentctl.CandidatePaths(configPath, name)
	primary := ""
	if len(paths) > 0 {
		primary = paths[0]
	}
	envPath := relayFile(configPath, name)
	ctlReq := agentctl.Request{Op: "pair", TTLSeconds: int(ttl.Seconds()), SSHUser: sshUser}
	resp, _, last, primaryErr := tryControlPair(ctx, paths, primary, ctlReq, controlWait(envPath, primary))
	if last == nil && primaryErr == nil && resp.Code != "" {
		hint := resp.Relay
		if hint == "" {
			hint = relayHint(envPath)
		}
		printCode(resp.Agent, resp.Code, resp.Expires, hint)
		return nil
	}
	loadPath := instCfg
	if loadPath == "" {
		loadPath = configPath
	}
	cfg, loadErr := config.LoadAgent(loadPath)
	if loadErr != nil {
		return fmt.Errorf("%s\n%s", pairFailure(primary, primaryErr, last), loadErr)
	}
	if namedInstance(name) {
		cfg = preferInstanceFile(cfg, envPath)
		if strings.TrimSpace(cfg.RelayAddr) == "" {
			return fmt.Errorf("%s\nnamed instance %s: its relay address could not be read from %s. RELAY_ADDR in the environment was not used", pairFailure(primary, primaryErr, last), name, envPath)
		}
	}
	nameFilled, err := fillPairName(cfg.Name)
	if err != nil {
		return err
	}
	cfg.Name = nameFilled
	secret, kind, credErr := cfg.Credential()
	if credErr != nil {
		return fmt.Errorf("%s\n%s", pairFailure(primary, primaryErr, last), credErr)
	}
	base, err := pairing.HTTPBase(cfg.RelayAddr)
	if err != nil {
		return err
	}
	clientURL, err := pairing.ClientURL(cfg.RelayAddr)
	if err != nil {
		return err
	}
	if cfg.SSHUser != "" && !agent.IsMachineAccount(cfg.SSHUser) {
		sshUser = cfg.SSHUser
	}
	if sshUser != "" && loadPath != "" {
		_ = config.SetSSHUser(loadPath, sshUser)
	}
	create := pairing.CreateRequest{
		Key:        secret,
		Agent:      cfg.Name,
		SSHUser:    sshUser,
		ClientURL:  clientURL,
		TTLSeconds: int(ttl.Seconds()),
	}
	if kind == "agent" {
		create.Auth = "agent"
	}
	created, err := pairing.PostCreate(ctx, base, create)
	if err != nil {
		return err
	}
	printCode(created.Agent, created.Code, created.ExpiresAt, clientURL)
	return nil
}

func runUnpair(args []string) error {
	fs := flag.NewFlagSet("kite-agent unpair", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := config.PeekConfigFlag(args)
	list := fs.Bool("list", false, "list keys this agent installed")
	revoke := fs.String("revoke", "", "remove keys tagged with this label")
	var serviceName string
	fs.StringVar(&configPath, "config", configPath, "agent config file (or KITE_CONFIG)")
	fs.StringVar(&serviceName, "service-name", "", "instance whose control file to use (default kite-agent)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite-agent unpair --list\n")
		fmt.Fprintf(fs.Output(), "       kite-agent unpair --revoke <label>\n\n")
		fmt.Fprintf(fs.Output(), "unpair lists or removes authorized_keys lines that kite pair installed.\n")
		fmt.Fprintf(fs.Output(), "This does not revoke the relay client token. On the relay also run:\n")
		fmt.Fprintf(fs.Output(), "  kite-relay tokens revoke <id-or-label>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *list == (*revoke != "") {
		return errors.New("pass either --list or --revoke")
	}
	name, err := agentsvc.CanonicalServiceName(serviceName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	op := "list"
	if *revoke != "" {
		op = "revoke"
	}
	for _, path := range agentctl.CandidatePaths(configPath, name) {
		resp, err := agentctl.Call(ctx, path, agentctl.Request{Op: op, Label: *revoke})
		if err != nil {
			continue
		}
		return printUnpair(resp, *revoke)
	}
	return directUnpair(configPath, *list, *revoke)
}

func printCode(agentName, code string, exp time.Time, relay string) {
	fmt.Fprintf(os.Stdout, "Agent: %s\n", agentName)
	fmt.Fprintf(os.Stdout, "Pairing code: %s\n", code)
	if !exp.IsZero() {
		fmt.Fprintf(os.Stdout, "Expires: %s (one time)\n", exp.In(time.Local).Format(time.RFC3339))
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "On the computer you connect from:")
	if relay != "" {
		fmt.Fprintf(os.Stdout, "  kite connect --relay %s %s\n", relay, code)
	} else {
		fmt.Fprintf(os.Stdout, "  kite connect --relay wss://tunnel.example.com %s\n", code)
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "The code is not the registration key.")
}

func printUnpair(resp agentctl.Response, label string) error {
	if label == "" {
		if len(resp.Keys) == 0 {
			fmt.Fprintln(os.Stdout, "No paired keys.")
			return nil
		}
		fmt.Fprintln(os.Stdout, "LABEL\tDATE\tFINGERPRINT")
		for _, k := range resp.Keys {
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", k.Label, k.Date, k.Fingerprint)
		}
		return nil
	}
	fmt.Fprintf(os.Stdout, "Removed %d key(s) labeled %s.\n", resp.Removed, label)
	fmt.Fprintln(os.Stdout, "The relay client token is separate. On the relay: kite-relay tokens revoke "+label)
	return nil
}

func directUnpair(configPath string, list bool, label string) error {
	cfg, err := config.LoadAgent(configPath)
	if err != nil {
		return err
	}
	if cfg.SSHUser == "" {
		cfg.SSHUser = defaultSSHUser()
	}
	path, kind, owner, err := agent.AuthorizedKeys(agent.Config{
		SSHUser: cfg.SSHUser,
	})
	if err != nil {
		return fmt.Errorf("%w\nIf the agent service is running, start it and retry so it can edit the file", err)
	}
	if list {
		recs, err := authkeys.List(path)
		if err != nil {
			return err
		}
		return printUnpair(agentctl.Response{Keys: recs}, "")
	}
	n, err := authkeys.Revoke(path, label, kind, owner)
	if err != nil {
		return err
	}
	return printUnpair(agentctl.Response{Removed: n}, label)
}

const pairControlWait = 10 * time.Second

func namedInstance(name string) bool {
	return strings.TrimSpace(name) != "" && !strings.EqualFold(name, agentsvc.ServiceName)
}

// controlWait is how long to retry the control file after install. The
// agent writes it and starts listening a moment after the service is up.
// Zero means the instance does not look installed, so pair should not pause.
func controlWait(envPath, controlPath string) time.Duration {
	if instanceLooksInstalled(envPath, controlPath) {
		return pairControlWait
	}
	return 0
}

func instanceLooksInstalled(envPath, controlPath string) bool {
	if pathExists(envPath) || pathExists(controlPath) {
		return true
	}
	return false
}

func pathExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil || os.IsPermission(err)
}

func tryControlPair(ctx context.Context, paths []string, primary string, req agentctl.Request, wait time.Duration) (agentctl.Response, string, error, error) {
	deadline := time.Now().Add(wait)
	var last, primaryErr error
	for {
		for _, path := range paths {
			resp, err := agentctl.Call(ctx, path, req)
			if err == nil {
				return resp, path, nil, nil
			}
			last = err
			if path == primary {
				primaryErr = err
			}
		}
		if wait <= 0 || !time.Now().Before(deadline) {
			return agentctl.Response{}, "", last, primaryErr
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return agentctl.Response{}, "", last, primaryErr
		case <-timer.C:
		}
	}
}

// preferInstanceFile keeps the named instance's relay and token. The shell
// RELAY_ADDR and REGISTRATION_KEY must not replace them.
func preferInstanceFile(cfg config.Agent, path string) config.Agent {
	relay, token, key, name := readInstanceSettings(path)
	if relay != "" {
		cfg.RelayAddr = relay
	} else {
		cfg.RelayAddr = ""
	}
	if token != "" {
		cfg.AgentToken = token
		cfg.TokenFromFile = true
		cfg.RegistrationKey = key
	} else if key != "" {
		cfg.RegistrationKey = key
		cfg.AgentToken = ""
		cfg.TokenFromFile = false
	} else {
		cfg.RegistrationKey = ""
		cfg.AgentToken = ""
		cfg.TokenFromFile = false
	}
	if name != "" {
		cfg.Name = name
	}
	return cfg
}

func readInstanceSettings(path string) (relay, token, key, name string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexAny(line, "=:")
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
		switch strings.ToLower(k) {
		case "relay_addr":
			relay = v
		case "agent_token":
			token = v
		case "registration_key":
			key = v
		case "agent_name":
			name = v
		}
	}
	return relay, token, key, name
}

func instanceConfig(flag, service string) string {
	if strings.TrimSpace(flag) != "" {
		return flag
	}
	if runtime.GOOS == "windows" {
		lay, err := agentsvc.DefaultLayout(service)
		if err == nil {
			return lay.ConfigPath
		}
	}
	return ""
}

func relayFile(flag, service string) string {
	if p := instanceConfig(flag, service); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return ""
	}
	name := service
	if name == "" || strings.EqualFold(name, agentsvc.ServiceName) {
		return "/etc/kite/agent.env"
	}
	return filepath.Join("/etc/kite", name, "agent.env")
}

func relayHint(configPath string) string {
	raw := config.FileRelay(configPath)
	if raw == "" {
		return ""
	}
	u, err := pairing.ClientURL(raw)
	if err != nil {
		return ""
	}
	return u
}

// fillPairName uses the hostname when the config has no agent name.
// kite-agent run already does this. pair must too, or the relay rejects
// the empty name.
func fillPairName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name != "" {
		return name, nil
	}
	return agent.DefaultAgentName()
}

func pairFailure(primary string, primaryErr, last error) string {
	shown := last
	if primaryErr != nil {
		shown = primaryErr
	}
	return pairHelp(shown, primary)
}

func pairHelp(err error, controlPath string) string {
	msg := `kite-agent pair could not reach the running agent.
Start kite-agent and retry. On Windows the installing user can pair without
a further elevation prompt while the service is running. On Linux and macOS,
run: sudo kite-agent pair
when the control file is not readable. An agent installed with --code pairs
with its saved agent token; it does not need REGISTRATION_KEY.`
	if strings.TrimSpace(controlPath) != "" {
		msg += "\nThe control file for this instance is " + controlPath + "."
	}
	if err != nil {
		return msg + "\nLast error: " + err.Error()
	}
	return msg
}

func defaultSSHUser() string {
	return agent.InvokingUser()
}

func rememberSSHUser(serviceName, configPath string) {
	userName := defaultSSHUser()
	if userName == "" {
		return
	}
	path := instanceConfig(configPath, serviceName)
	if path == "" {
		return
	}
	_ = config.SetSSHUser(path, userName)
}
