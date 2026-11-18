// Package connect implements `kite connect <code>`: redeem a pairing code,
// keep the private key locally, and write the client and ssh configs.
package connect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/clientkey"
	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/pairing"
	"github.com/zzzbug01/kite/internal/sshcfg"
)

// Options is one pairing redemption.
type Options struct {
	Code          string
	RelayURL      string
	Label         string
	ConfigPath    string
	SSHConfigPath string
	Executable    string
	IdentityDir   string // directory for ~/.ssh; default is the ssh config directory
	Now           time.Time
}

// Result is what the command prints. The client token is not included; it
// is written to the config file only.
type Result struct {
	Agent      string
	User       string
	RelayURL   string
	Identity   string
	KeyBackup  string
	ConfigPath string
	SSHConfig  string
	SSHBackup  string
	HostAdded  bool
}

// Run redeems code. The private key is generated on this machine and is
// not part of the redeem request; only the public key is sent.
func Run(ctx context.Context, opt Options) (Result, error) {
	if _, err := pairing.NormalizeCode(opt.Code); err != nil {
		return Result{}, fmt.Errorf("pairing code must look like ABCD-EFGH")
	}
	if strings.TrimSpace(opt.RelayURL) == "" {
		return Result{}, fmt.Errorf("relay URL is required the first time; pass --relay wss://tunnel.example.com or set RELAY_ADDR")
	}
	if strings.TrimSpace(opt.Executable) == "" {
		return Result{}, fmt.Errorf("path to the kite binary is required")
	}
	if strings.TrimSpace(opt.ConfigPath) == "" || strings.TrimSpace(opt.SSHConfigPath) == "" {
		return Result{}, fmt.Errorf("config paths are required")
	}
	base, err := pairing.HTTPBase(opt.RelayURL)
	if err != nil {
		return Result{}, err
	}
	label := opt.Label
	if strings.TrimSpace(label) == "" {
		label = defaultLabel()
	}
	pemBytes, pub, err := clientkey.Generate()
	if err != nil {
		return Result{}, err
	}
	resp, err := pairing.PostRedeem(ctx, base, pairing.RedeemRequest{
		Code:      opt.Code,
		PublicKey: pub,
		Label:     label,
	})
	if err != nil {
		return Result{}, err
	}
	if resp.Agent == "" || resp.ClientToken == "" || resp.RelayURL == "" {
		return Result{}, fmt.Errorf("relay redeem response was incomplete")
	}
	if strings.TrimSpace(resp.SSHUser) == "" {
		return Result{}, fmt.Errorf("relay did not return an SSH user")
	}
	identDir := opt.IdentityDir
	if identDir == "" {
		identDir = filepath.Dir(opt.SSHConfigPath)
	}
	ident := filepath.Join(identDir, "kite_"+safeFile(resp.Agent))
	backup, err := clientkey.Save(ident, pemBytes)
	if err != nil {
		return Result{}, fmt.Errorf("save identity: %w (the key was installed on the agent; pair again and keep this machine's new key)", err)
	}
	if err := config.MergeClient(opt.ConfigPath, config.ClientUpdate{
		RelayAddr:    resp.RelayURL,
		Agent:        resp.Agent,
		Token:        resp.ClientToken,
		User:         resp.SSHUser,
		IdentityFile: ident,
	}); err != nil {
		return Result{}, fmt.Errorf("save client config: %w (the key was installed; pair again if this file was not written)", err)
	}
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	sshRes, err := sshcfg.Merge(opt.SSHConfigPath, sshcfg.Host{
		Alias:        resp.Agent,
		User:         resp.SSHUser,
		IdentityFile: ident,
		ProxyCommand: opt.Executable,
	}, now)
	if err != nil {
		return Result{}, fmt.Errorf("ssh config: %w (client credential is in %s)", err, opt.ConfigPath)
	}
	return Result{
		Agent:      resp.Agent,
		User:       resp.SSHUser,
		RelayURL:   resp.RelayURL,
		Identity:   ident,
		KeyBackup:  backup,
		ConfigPath: opt.ConfigPath,
		SSHConfig:  sshRes.Path,
		SSHBackup:  sshRes.Backup,
		HostAdded:  sshRes.Changed,
	}, nil
}

func defaultLabel() string {
	h, err := os.Hostname()
	if err != nil {
		return "client"
	}
	return pairing.SanitizeLabel(h)
}

func safeFile(name string) string {
	return pairing.SanitizeLabel(name)
}
