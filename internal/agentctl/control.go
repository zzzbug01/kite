// Package agentctl is the loopback control socket the running kite-agent
// serves so `kite-agent pair` can ask for a code without a new elevation
// prompt. The socket accepts connections only from 127.0.0.1 and requires
// the token stored next to the agent config.
package agentctl

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/authkeys"
)

// Endpoint is the file a local client reads. Addr is always a loopback
// host:port. Token authorizes the caller. The file mode is 0600.
type Endpoint struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

// Request is one control operation.
type Request struct {
	Token      string `json:"token"`
	Op         string `json:"op"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
	Label      string `json:"label,omitempty"`
}

// Response is one control result. Code is present only for a successful pair.
type Response struct {
	OK      bool              `json:"ok"`
	Error   string            `json:"error,omitempty"`
	Code    string            `json:"code,omitempty"`
	Agent   string            `json:"agent,omitempty"`
	Expires time.Time         `json:"expires_at,omitempty"`
	Keys    []authkeys.Record `json:"keys,omitempty"`
	Removed int               `json:"removed,omitempty"`
}

// Handler implements pair and unpair against the running agent.
type Handler interface {
	Pair(ctx context.Context, ttl time.Duration) (code, agent string, expires time.Time, err error)
	List(ctx context.Context) ([]authkeys.Record, error)
	Revoke(ctx context.Context, label string) (int, error)
}

// Start listens on 127.0.0.1:0, writes the endpoint file, and serves until
// ctx is cancelled. The returned function stops the listener.
func Start(ctx context.Context, path string, h Handler) (func(), error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("control file path is required")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		ln.Close()
		return nil, err
	}
	ep := Endpoint{Addr: ln.Addr().String(), Token: token}
	if err := writeEndpoint(path, ep); err != nil {
		ln.Close()
		return nil, err
	}
	done := make(chan struct{})
	go serve(ctx, ln, token, h, done)
	stop := func() {
		ln.Close()
		<-done
		os.Remove(path)
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	return stop, nil
}

func serve(ctx context.Context, ln net.Listener, token string, h Handler, done chan struct{}) {
	defer close(done)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			var req Request
			dec := json.NewDecoder(io.LimitReader(c, 4096))
			if err := dec.Decode(&req); err != nil {
				writeResp(c, Response{Error: "bad request"})
				return
			}
			if !tokenOK(req.Token, token) {
				writeResp(c, Response{Error: "unauthorized"})
				return
			}
			switch req.Op {
			case "pair":
				code, agent, exp, err := h.Pair(ctx, time.Duration(req.TTLSeconds)*time.Second)
				if err != nil {
					writeResp(c, Response{Error: err.Error()})
					return
				}
				writeResp(c, Response{OK: true, Code: code, Agent: agent, Expires: exp})
			case "list":
				keys, err := h.List(ctx)
				if err != nil {
					writeResp(c, Response{Error: err.Error()})
					return
				}
				writeResp(c, Response{OK: true, Keys: keys})
			case "revoke":
				n, err := h.Revoke(ctx, req.Label)
				if err != nil {
					writeResp(c, Response{Error: err.Error()})
					return
				}
				writeResp(c, Response{OK: true, Removed: n})
			default:
				writeResp(c, Response{Error: "unknown op"})
			}
		}(conn)
	}
}

func writeResp(c net.Conn, resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = c.Write(b)
}

func tokenOK(got, want string) bool {
	if want == "" {
		return false
	}
	sumGot := sha256.Sum256([]byte(got))
	sumWant := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(sumGot[:], sumWant[:]) == 1
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func writeEndpoint(path string, ep Endpoint) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(ep)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			os.Remove(tmp)
			return err2
		}
	}
	return os.Chmod(path, 0o600)
}

// Call reads the endpoint file and performs one operation.
// The address in the file must be loopback; a file that points elsewhere is rejected
// so the token is not sent off the machine.
func Call(ctx context.Context, path string, req Request) (Response, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Response{}, err
	}
	var ep Endpoint
	if err := json.Unmarshal(b, &ep); err != nil {
		return Response{}, fmt.Errorf("control file: %w", err)
	}
	if !loopback(ep.Addr) {
		return Response{}, fmt.Errorf("control file address %q is not loopback", ep.Addr)
	}
	req.Token = ep.Token
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", ep.Addr)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err := conn.Write(body); err != nil {
		return Response{}, err
	}
	if c, ok := conn.(*net.TCPConn); ok {
		_ = c.CloseWrite()
	}
	var resp Response
	dec := json.NewDecoder(io.LimitReader(conn, 1<<20))
	if err := dec.Decode(&resp); err != nil {
		return Response{}, err
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "control request failed"
		}
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CandidatePaths are the control files `kite-agent pair` tries, in order.
func CandidatePaths(configFlag, serviceName string) []string {
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		for _, existing := range out {
			if same(existing, p) {
				return
			}
		}
		out = append(out, p)
	}
	if configFlag != "" {
		add(filepath.Join(filepath.Dir(configFlag), "agent-control.json"))
	}
	if runtime.GOOS == "windows" {
		root := strings.TrimSpace(os.Getenv("ProgramData"))
		if root == "" {
			root = `C:\ProgramData`
		}
		name := strings.TrimSpace(serviceName)
		if name != "" && !strings.EqualFold(name, "kite-agent") {
			add(filepath.Join(root, "kite", name, "agent-control.json"))
		}
		add(filepath.Join(root, "kite", "agent-control.json"))
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		add(filepath.Join(dir, "kite", "agent-control.json"))
	}
	if runtime.GOOS != "windows" {
		add("/var/lib/kite/agent-control.json")
	}
	return out
}

func same(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
