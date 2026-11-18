package pairing

import (
	"crypto/rand"
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
)

// DefaultStateDir is where the relay keeps hashed client tokens.
// Windows uses %ProgramData%\kite\state when STATE_DIR is unset; callers
// pass the resolved path into Open.
func DefaultStateDir() string {
	if runtime.GOOS == "windows" {
		root := strings.TrimSpace(os.Getenv("ProgramData"))
		if root == "" {
			root = `C:\ProgramData`
		}
		return filepath.Join(root, "kite", "state")
	}
	return "/var/lib/kite"
}

// Record is one paired client. TokenHash is hex-encoded SHA-256. The raw
// token is not stored.
type Record struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"token_hash"`
	Agent     string    `json:"agent"`
	Label     string    `json:"label"`
	SSHUser   string    `json:"ssh_user,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is a JSON file of client-token hashes. The running relay and
// `kite-relay tokens` share it. A directory lock serializes updates.
type Store struct {
	dir  string
	path string
}

type fileDoc struct {
	Clients []Record `json:"clients"`
}

// Open prepares a store under dir. The directory is created on Ensure.
func Open(dir string) *Store {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultStateDir()
	}
	return &Store{dir: dir, path: filepath.Join(dir, "clients.json")}
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

// Issue creates a token, stores its hash, and returns the raw token once.
func (s *Store) Issue(agent, label, sshUser string, now time.Time) (raw string, rec Record, err error) {
	if err := s.Ensure(); err != nil {
		return "", Record{}, err
	}
	unlock, err := acquire(s.dir)
	if err != nil {
		return "", Record{}, err
	}
	defer unlock()
	doc, err := s.read()
	if err != nil {
		return "", Record{}, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", Record{}, err
	}
	raw = "kite_" + hex.EncodeToString(buf)
	idRaw := make([]byte, 4)
	if _, err := rand.Read(idRaw); err != nil {
		return "", Record{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rec = Record{
		ID:        "c_" + hex.EncodeToString(idRaw),
		TokenHash: hex.EncodeToString(hashToken(raw)),
		Agent:     agent,
		Label:     SanitizeLabel(label),
		SSHUser:   sshUser,
		CreatedAt: now.UTC(),
	}
	doc.Clients = append(doc.Clients, rec)
	if err := s.write(doc); err != nil {
		return "", Record{}, err
	}
	return raw, rec, nil
}

// Authenticate reports whether raw is a live token for agent.
// A token for a different agent is rejected the same way as an unknown token.
func (s *Store) Authenticate(raw, agent string) (Record, bool) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(agent) == "" {
		return Record{}, false
	}
	unlock, err := acquire(s.dir)
	if err != nil {
		return Record{}, false
	}
	defer unlock()
	doc, err := s.read()
	if err != nil {
		return Record{}, false
	}
	sum := hashToken(raw)
	var found Record
	ok := false
	for _, rec := range doc.Clients {
		h, err := hex.DecodeString(rec.TokenHash)
		if err != nil || len(h) != len(sum) {
			continue
		}
		if subtle.ConstantTimeCompare(h, sum) == 1 {
			found = rec
			ok = true
		}
	}
	if !ok || found.Agent != agent {
		return Record{}, false
	}
	return found, true
}

// List returns every client record, oldest first. Hashes are included for
// the file format; callers that print for humans should omit TokenHash.
func (s *Store) List() ([]Record, error) {
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
	out := append([]Record(nil), doc.Clients...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// Revoke removes the client with id, or the single client with label.
// An ambiguous label returns an error that names the matching ids.
func (s *Store) Revoke(idOrLabel string) (Record, error) {
	idOrLabel = strings.TrimSpace(idOrLabel)
	if idOrLabel == "" {
		return Record{}, errors.New("token id or label is required")
	}
	if err := s.Ensure(); err != nil {
		return Record{}, err
	}
	unlock, err := acquire(s.dir)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	doc, err := s.read()
	if err != nil {
		return Record{}, err
	}
	match := -1
	var labelHits []Record
	for i, rec := range doc.Clients {
		if rec.ID == idOrLabel {
			match = i
			break
		}
		if rec.Label == idOrLabel || rec.Label == SanitizeLabel(idOrLabel) {
			labelHits = append(labelHits, rec)
		}
	}
	if match < 0 {
		switch len(labelHits) {
		case 0:
			return Record{}, fmt.Errorf("no client token %q", idOrLabel)
		case 1:
			for i, rec := range doc.Clients {
				if rec.ID == labelHits[0].ID {
					match = i
					break
				}
			}
		default:
			ids := make([]string, len(labelHits))
			for i, rec := range labelHits {
				ids[i] = rec.ID
			}
			return Record{}, fmt.Errorf("label %q matches %s; revoke by id", idOrLabel, strings.Join(ids, ", "))
		}
	}
	removed := doc.Clients[match]
	doc.Clients = append(doc.Clients[:match], doc.Clients[match+1:]...)
	if err := s.write(doc); err != nil {
		return Record{}, err
	}
	removed.TokenHash = ""
	return removed, nil
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
	path := filepath.Join(dir, ".clients.lock")
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
			return nil, errors.New("timed out waiting for the token store lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
