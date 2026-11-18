// Package clientkey creates and loads the ed25519 key kite connect keeps
// on the laptop. The private key is never sent anywhere.
package clientkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Generate creates a new ed25519 key in memory. The private key PEM is
// returned to the caller and is not written here.
func Generate() (pemBytes []byte, publicLine string, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "kite")
	if err != nil {
		return nil, "", err
	}
	pemBytes = pem.EncodeToMemory(block)
	publicLine, err = publicFromPrivate(pemBytes)
	if err != nil {
		return nil, "", err
	}
	return pemBytes, publicLine, nil
}

// Save writes a private key PEM to path with mode 0600. An existing file is
// moved aside and the backup path is returned.
func Save(path string, pemBytes []byte) (backup string, err error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("identity path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		backup = path + ".bak." + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(path, backup); err != nil {
			return "", err
		}
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backup, err
	}
	_, werr := f.Write(pemBytes)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		if werr != nil {
			return backup, werr
		}
		return backup, cerr
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return backup, err
	}
	if err := tighten(path); err != nil {
		return backup, err
	}
	return backup, nil
}

// LoadOrCreate returns the authorized_keys line (without a comment) for the
// private key at path. An existing file is reused. A new file is mode 0600.
func LoadOrCreate(path string) (publicLine string, created bool, err error) {
	if strings.TrimSpace(path) == "" {
		return "", false, errors.New("identity path is required")
	}
	if b, err := os.ReadFile(path); err == nil {
		line, err := publicFromPrivate(b)
		if err != nil {
			return "", false, err
		}
		if err := tighten(path); err != nil {
			return "", false, err
		}
		return line, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", false, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "kite")
	if err != nil {
		return "", false, err
	}
	pemBytes := pem.EncodeToMemory(block)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", false, err
	}
	_, werr := f.Write(pemBytes)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		if werr != nil {
			return "", false, werr
		}
		return "", false, cerr
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return "", false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", false, err
	}
	if err := tighten(path); err != nil {
		return "", false, err
	}
	line, err := publicFromPrivate(pemBytes)
	if err != nil {
		return "", false, err
	}
	return line, true, nil
}

func publicFromPrivate(pemBytes []byte) (string, error) {
	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		return "", fmt.Errorf("identity file: %w", err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return "", errors.New("identity file is not an ed25519 key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}
