// Package invite stores one-time install codes and the per-agent tokens
// those codes issue.
//
// kite-relay invite writes STATE_DIR/install.json while the relay is running.
// POST /invite/redeem, served by that same process, reads the file back.
// Both sides take an exclusive directory lock at STATE_DIR/.install.lock, so
// a code created on disk is visible to the running relay immediately and is
// still there after a restart. Pairing codes are different: those live only
// in memory and a restart drops them.
//
// The file holds SHA-256 hashes only. The raw code and the raw agent token
// are returned once and are not written down. A code is marked used only
// after the agent token has been stored.
package invite

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/pairing"
	"github.com/zzzbug01/kite/internal/protocol"
)

const (
	// FileName is the JSON document under STATE_DIR.
	FileName = "install.json"
	// DefaultTTL is how long a new install code stays valid.
	DefaultTTL = time.Hour
	// MaxTTL is the longest code lifetime invite will store.
	MaxTTL = 24 * time.Hour
	// MinTTL rejects a code that would expire immediately.
	MinTTL = time.Minute

	maxCodes = 100
)

var (
	// ErrInvalid means the code is unknown. The error text does not include it.
	ErrInvalid = errors.New("install code is not valid")
	// ErrExpired means the code was real and its lifetime ended.
	ErrExpired = errors.New("install code expired")
	// ErrUsed means the code was already redeemed.
	ErrUsed = errors.New("install code already used")
	// ErrName means the code was issued for a different agent name.
	// The code is not consumed.
	ErrName = errors.New("install code was issued for a different agent name")
	// ErrTooMany means too many unused codes are already outstanding.
	ErrTooMany = errors.New("too many outstanding install codes")
)

// AgentInfo is one installed agent. The token hash is not included.
type AgentInfo struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// Store is the on-disk install codes and agent tokens.
type Store struct {
	dir  string
	path string
}

type codeRec struct {
	Hash      string    `json:"hash"`
	Name      string    `json:"name,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used,omitempty"`
}

type agentRec struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"token_hash"`
	CreatedAt time.Time `json:"created_at"`
}

type fileDoc struct {
	Codes  []codeRec  `json:"codes,omitempty"`
	Agents []agentRec `json:"agents,omitempty"`
}

// Open prepares a store under dir. The directory is created on the first write.
func Open(dir string) *Store {
	if strings.TrimSpace(dir) == "" {
		dir = pairing.DefaultStateDir()
	}
	return &Store{dir: dir, path: filepath.Join(dir, FileName)}
}

// Dir is the state directory.
func (s *Store) Dir() string { return s.dir }

// Ensure creates the state directory with mode 0700.
func (s *Store) Ensure() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(s.dir, 0o700)
}

// Issue creates a single-use code. name, when set, is the only agent that
// may redeem it. ttl is the lifetime from now. The returned string is the
// only copy of the raw code.
func (s *Store) Issue(name string, ttl time.Duration, now time.Time) (string, time.Time, error) {
	name = strings.TrimSpace(name)
	if name != "" {
		if err := protocol.CheckAgentName(name); err != nil {
			return "", time.Time{}, err
		}
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return "", time.Time{}, fmt.Errorf("ttl must be between %s and %s", MinTTL, MaxTTL)
	}
	if now.IsZero() {
		now = time.Now()
	}
	code, err := pairing.GenerateCode()
	if err != nil {
		return "", time.Time{}, err
	}
	norm, err := pairing.NormalizeCode(code)
	if err != nil {
		return "", time.Time{}, err
	}
	exp := now.Add(ttl).UTC()
	if err := s.Ensure(); err != nil {
		return "", time.Time{}, err
	}
	unlock, err := acquire(s.dir)
	if err != nil {
		return "", time.Time{}, err
	}
	defer unlock()
	doc, err := s.read()
	if err != nil {
		return "", time.Time{}, err
	}
	doc.Codes = sweep(doc.Codes, now)
	outstanding := 0
	for _, c := range doc.Codes {
		if !c.Used {
			outstanding++
		}
	}
	if outstanding >= maxCodes {
		return "", time.Time{}, ErrTooMany
	}
	doc.Codes = append(doc.Codes, codeRec{
		Hash:      hex.EncodeToString(hashCode(norm)),
		Name:      name,
		ExpiresAt: exp,
	})
	if err := s.write(doc); err != nil {
		return "", time.Time{}, err
	}
	return code, exp, nil
}

// Redeem checks code, and on success stores a new agent token bound to the
// agent name and only then marks the code used. A mismatch, an expired code,
// or a write failure leaves the code usable until it expires.
func (s *Store) Redeem(code, name string, now time.Time) (token, agent string, err error) {
	name = strings.TrimSpace(name)
	if now.IsZero() {
		now = time.Now()
	}
	if err := s.Ensure(); err != nil {
		return "", "", err
	}
	unlock, err := acquire(s.dir)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	doc, err := s.read()
	if err != nil {
		return "", "", err
	}
	norm, nerr := pairing.NormalizeCode(code)
	sum := hashCode("AAAAAAAA")
	if nerr == nil {
		sum = hashCode(norm)
	}
	hashes := make([][]byte, len(doc.Codes))
	for i, c := range doc.Codes {
		h, decErr := hex.DecodeString(c.Hash)
		if decErr == nil {
			hashes[i] = h
		}
	}
	idx := matchHash(hashes, sum)
	if nerr != nil || idx < 0 {
		return "", "", ErrInvalid
	}
	rec := doc.Codes[idx]
	if rec.Used {
		return "", "", ErrUsed
	}
	if !now.Before(rec.ExpiresAt) {
		return "", "", ErrExpired
	}
	agent = name
	if rec.Name != "" {
		if name != "" && name != rec.Name {
			return "", "", ErrName
		}
		agent = rec.Name
	}
	if err := protocol.CheckAgentName(agent); err != nil {
		return "", "", err
	}
	raw, id, err := newToken()
	if err != nil {
		return "", "", err
	}
	next := make([]agentRec, 0, len(doc.Agents)+1)
	for _, a := range doc.Agents {
		if a.Name != agent {
			next = append(next, a)
		}
	}
	next = append(next, agentRec{
		ID:        id,
		Name:      agent,
		TokenHash: hex.EncodeToString(hashToken(raw)),
		CreatedAt: now.UTC(),
	})
	doc.Agents = next
	doc.Codes[idx].Used = true
	if err := s.write(doc); err != nil {
		return "", "", err
	}
	return raw, agent, nil
}

// Authenticate reports whether raw is the live token for name.
// A token for a different agent is rejected the same way as an unknown token.
func (s *Store) Authenticate(raw, name string) bool {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(name) == "" {
		return false
	}
	unlock, err := acquire(s.dir)
	if err != nil {
		return false
	}
	defer unlock()
	doc, err := s.read()
	if err != nil {
		return false
	}
	sum := hashToken(raw)
	found := ""
	ok := false
	for _, rec := range doc.Agents {
		h, err := hex.DecodeString(rec.TokenHash)
		if err != nil || len(h) != len(sum) {
			continue
		}
		if subtle.ConstantTimeCompare(h, sum) == 1 {
			found = rec.Name
			ok = true
		}
	}
	_ = subtle.ConstantTimeCompare(sum, make([]byte, len(sum)))
	return ok && found == name
}

// List returns installed agents, oldest first. Tokens are not included.
func (s *Store) List() ([]AgentInfo, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}
	unlock, err := acquire(s.dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	doc, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]AgentInfo, 0, len(doc.Agents))
	for _, rec := range doc.Agents {
		out = append(out, AgentInfo{ID: rec.ID, Name: rec.Name, CreatedAt: rec.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Name < out[j].Name
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// Revoke removes the agent token for name. Client tokens and SSH keys are
// left alone.
func (s *Store) Revoke(name string) (AgentInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AgentInfo{}, errors.New("agent name is required")
	}
	if err := s.Ensure(); err != nil {
		return AgentInfo{}, err
	}
	unlock, err := acquire(s.dir)
	if err != nil {
		return AgentInfo{}, err
	}
	defer unlock()
	doc, err := s.read()
	if err != nil {
		return AgentInfo{}, err
	}
	match := -1
	for i, rec := range doc.Agents {
		if rec.Name == name {
			match = i
			break
		}
	}
	if match < 0 {
		return AgentInfo{}, fmt.Errorf("no agent token %q", name)
	}
	removed := doc.Agents[match]
	doc.Agents = append(doc.Agents[:match], doc.Agents[match+1:]...)
	if err := s.write(doc); err != nil {
		return AgentInfo{}, err
	}
	return AgentInfo{ID: removed.ID, Name: removed.Name, CreatedAt: removed.CreatedAt}, nil
}

func sweep(codes []codeRec, now time.Time) []codeRec {
	out := codes[:0]
	for _, c := range codes {
		// Keep a used or recently expired code so a retry can say why it failed.
		// Drop codes that expired more than MaxTTL ago, used or not.
		if now.After(c.ExpiresAt.Add(MaxTTL)) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (s *Store) read() (fileDoc, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileDoc{}, nil
		}
		return fileDoc{}, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return fileDoc{}, nil
	}
	var doc fileDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return fileDoc{}, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return doc, nil
}

func (s *Store) write(doc fileDoc) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := fmt.Sprintf("%s.%d.tmp", s.path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		os.Remove(tmp)
		if werr != nil {
			return werr
		}
		if serr != nil {
			return serr
		}
		return cerr
	}
	if err := replaceFile(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Chmod(s.path, 0o600)
}

func replaceFile(tmp, dst string) error {
	if runtime.GOOS == "windows" {
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

func acquire(dir string) (func(), error) {
	path := filepath.Join(dir, ".install.lock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			return func() { _ = os.Remove(path) }, nil
		}
		if st, statErr := os.Stat(path); statErr == nil && time.Since(st.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for the install-code lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func hashCode(norm string) []byte {
	sum := sha256.Sum256([]byte("kite-install-code-v1:" + norm))
	return sum[:]
}

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte("kite-agent-token-v1:" + raw))
	return sum[:]
}

func newToken() (raw, id string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	idRaw := make([]byte, 4)
	if _, err = rand.Read(idRaw); err != nil {
		return "", "", err
	}
	return "kite_agent_" + hex.EncodeToString(buf), "a_" + hex.EncodeToString(idRaw), nil
}

// matchHash returns the index of sum in hashes, or -1. It always compares
// against every entry and one dummy, so a hit on the first code is not a
// shorter compare than a miss.
func matchHash(hashes [][]byte, sum []byte) int {
	found := -1
	var dummy [32]byte
	for i, h := range hashes {
		if len(h) == len(sum) && subtle.ConstantTimeCompare(h, sum) == 1 {
			found = i
		}
	}
	_ = subtle.ConstantTimeCompare(sum, dummy[:])
	return found
}
