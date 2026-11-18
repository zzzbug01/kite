// Package client is the kite SSH ProxyCommand side. Dial opens one stream to a
// named agent and exposes it as a byte pipe.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/protocol"
	"github.com/zzzbug01/kite/internal/wsutil"
)

// Sentinel errors mapped from relay reason strings.
var (
	ErrUnauthorized   = errors.New("unauthorized")
	ErrAgentNotFound  = errors.New("agent not found")
	ErrTooManyStreams = errors.New("too many streams")
)

// Config selects the relay and the agent to reach.
type Config struct {
	RelayURL        string
	RegistrationKey string
	Agent           string
	IdleTimeout     time.Duration
	PingInterval    time.Duration
	Logger          *slog.Logger
	// HTTPClient overrides the handshake client. Production leaves it nil.
	HTTPClient *http.Client
	// Auth is "registration" (default) or "client". "client" means
	// RegistrationKey holds a per-client token from `kite connect`.
	Auth string
}

// Stream is one SSH session. Read and Write may be used concurrently.
// CloseWrite signals EOF to the remote SSH server without closing the read side.
type Stream struct {
	ws     *websocket.Conn
	id     uint32
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cond    *sync.Cond
	buf     []byte
	eof     bool
	readErr error

	writeMu sync.Mutex
	closed  bool

	endOnce   sync.Once
	closeOnce sync.Once

	lastRecv atomic.Int64
	idle     time.Duration
	ping     time.Duration
	log      *slog.Logger
}

// Dial connects to the relay, authenticates, and opens a stream to cfg.Agent.
// ctx bounds the handshake and, once Dial returns, the life of the stream:
// cancelling it closes the stream.
func Dial(ctx context.Context, cfg Config) (*Stream, error) {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = protocol.DefaultIdleTimeout
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = protocol.PingInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if !protocol.ValidName(cfg.Agent) {
		return nil, errors.New("agent name is required")
	}
	url, err := config.NormalizeRelayURL(cfg.RelayURL, "/client")
	if err != nil {
		return nil, err
	}
	ws, err := wsutil.DialHTTP(ctx, url, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	helloMsg := protocol.Hello{
		Role:  "client",
		Key:   cfg.RegistrationKey,
		Agent: cfg.Agent,
	}
	if cfg.Auth == "client" {
		helloMsg.Auth = "client"
	}
	hello, err := protocol.MarshalHello(helloMsg)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	if err := wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypeHello, Payload: hello}); err != nil {
		ws.CloseNow()
		return nil, err
	}

	timer := time.AfterFunc(20*time.Second, func() { ws.CloseNow() })
	defer timer.Stop()
	for {
		f, err := wsutil.ReadFrame(ctx, ws)
		if err != nil {
			ws.CloseNow()
			return nil, err
		}
		switch f.Type {
		case protocol.TypePing:
			if err := wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypePong}); err != nil {
				ws.CloseNow()
				return nil, err
			}
		case protocol.TypePong:
		case protocol.TypeHelloErr, protocol.TypeOpenErr:
			ws.CloseNow()
			err := mapReason(string(f.Payload))
			if errors.Is(err, ErrUnauthorized) && cfg.Auth == "client" {
				err = fmt.Errorf("%w; the relay rejected the client token (upgrade kite-relay, or run kite connect again if it was revoked)", err)
			}
			return nil, err
		case protocol.TypeOpenOK:
			if f.StreamID == 0 {
				ws.CloseNow()
				return nil, errors.New("relay opened stream 0")
			}
			s := newStream(ctx, ws, f.StreamID, cfg)
			wsutil.Touch(&s.lastRecv)
			go s.readLoop()
			go wsutil.Heartbeat(s.ctx, s.ping, func() error {
				return wsutil.WriteFrame(s.ctx, s.ws, protocol.Frame{Type: protocol.TypePing})
			})
			go wsutil.WatchIdle(s.ctx, &s.lastRecv, s.idle, func() {
				s.log.Info("relay idle timeout")
				_ = s.Close()
			})
			s.log.Info("connected", "agent", cfg.Agent, "stream", s.id)
			return s, nil
		default:
			ws.CloseNow()
			return nil, errors.New("unexpected relay frame during handshake")
		}
	}
}

func newStream(ctx context.Context, ws *websocket.Conn, id uint32, cfg Config) *Stream {
	sctx, cancel := context.WithCancel(ctx)
	s := &Stream{
		ws:     ws,
		id:     id,
		ctx:    sctx,
		cancel: cancel,
		idle:   cfg.IdleTimeout,
		ping:   cfg.PingInterval,
		log:    cfg.Logger,
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func mapReason(msg string) error {
	switch msg {
	case protocol.ReasonUnauthorized:
		return ErrUnauthorized
	case protocol.ReasonAgentNotFound:
		return ErrAgentNotFound
	case protocol.ReasonTooManyStreams:
		return ErrTooManyStreams
	case "":
		return errors.New("relay rejected the stream")
	default:
		return errors.New(msg)
	}
}

// Read returns bytes from the agent. It returns io.EOF when the remote write
// side closes.
func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) == 0 && !s.eof && s.readErr == nil {
		s.cond.Wait()
	}
	if len(s.buf) == 0 {
		if s.eof {
			return 0, io.EOF
		}
		if s.readErr != nil {
			return 0, s.readErr
		}
		return 0, io.EOF
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	if len(s.buf) == 0 && cap(s.buf) > 64*1024 {
		s.buf = nil
	}
	s.cond.Broadcast()
	return n, nil
}

// Write sends bytes to the agent's local SSH server.
func (s *Stream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return 0, net.ErrClosed
	}
	sent := 0
	for len(p) > 0 {
		n := len(p)
		if n > protocol.ChunkSize {
			n = protocol.ChunkSize
		}
		err := wsutil.WriteFrame(s.ctx, s.ws, protocol.Frame{
			Type:     protocol.TypeData,
			StreamID: s.id,
			Payload:  p[:n],
		})
		if err != nil {
			if sent == 0 {
				return 0, err
			}
			return sent, err
		}
		sent += n
		p = p[n:]
	}
	return sent, nil
}

// CloseWrite tells the remote SSH server that no more bytes will be written.
func (s *Stream) CloseWrite() error {
	var err error
	s.endOnce.Do(func() {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		if s.closed {
			err = net.ErrClosed
			return
		}
		err = wsutil.WriteFrame(s.ctx, s.ws, protocol.Frame{
			Type:     protocol.TypeEnd,
			StreamID: s.id,
		})
	})
	return err
}

// Close tears down the stream.
func (s *Stream) Close() error {
	s.closeOnce.Do(func() {
		s.writeMu.Lock()
		s.closed = true
		s.writeMu.Unlock()
		_ = wsutil.WriteFrame(context.Background(), s.ws, protocol.Frame{
			Type:     protocol.TypeClose,
			StreamID: s.id,
		})
		s.cancel()
		s.ws.CloseNow()
		s.mu.Lock()
		if s.readErr == nil {
			s.readErr = net.ErrClosed
		}
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	return nil
}

func (s *Stream) readLoop() {
	defer func() {
		s.mu.Lock()
		if s.readErr == nil {
			s.eof = true
		}
		s.cond.Broadcast()
		s.mu.Unlock()
	}()
	for {
		f, err := wsutil.ReadFrame(s.ctx, s.ws)
		if err != nil {
			s.mu.Lock()
			if s.readErr == nil && !s.eof {
				if s.ctx.Err() != nil {
					s.readErr = net.ErrClosed
				} else {
					s.readErr = err
				}
			}
			s.cond.Broadcast()
			s.mu.Unlock()
			return
		}
		wsutil.Touch(&s.lastRecv)
		switch f.Type {
		case protocol.TypePing:
			_ = wsutil.WriteFrame(s.ctx, s.ws, protocol.Frame{Type: protocol.TypePong})
		case protocol.TypePong:
		case protocol.TypeData:
			if f.StreamID != s.id || len(f.Payload) == 0 {
				continue
			}
			s.enqueue(f.Payload)
		case protocol.TypeEnd, protocol.TypeClose:
			s.mu.Lock()
			s.eof = true
			s.cond.Broadcast()
			s.mu.Unlock()
			if f.Type == protocol.TypeClose {
				return
			}
		default:
		}
	}
}

func (s *Stream) enqueue(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) > 1024*1024 && !s.eof && s.readErr == nil {
		s.cond.Wait()
	}
	if s.eof || s.readErr != nil {
		return
	}
	s.buf = append(s.buf, p...)
	s.cond.Broadcast()
}

// Pipe copies stdin to the stream and the stream to stdout until the remote
// side closes. Stdin EOF half-closes the stream so the remote SSH server can
// finish writing. Pipe returns when the remote side closes; it does not wait
// for stdin, because ProxyCommand must exit even if ssh still holds the input
// pipe open. The process exit reaps the copy goroutine.
func Pipe(ctx context.Context, st *Stream, stdin io.Reader, stdout io.Writer) error {
	go func() {
		_, err := io.Copy(st, stdin)
		if err == nil {
			_ = st.CloseWrite()
		}
	}()
	_, err := io.Copy(stdout, st)
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		err = nil
	}
	_ = st.Close()
	if ctx.Err() != nil && err == nil {
		return ctx.Err()
	}
	return err
}
