// Package agent is the kite-agent: it dials out to a relay and forwards each
// opened stream to the configured local SSH address.
package agent

import (
	"context"
	"errors"
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

// Config is a resolved agent configuration plus test knobs.
type Config struct {
	RelayURL        string
	RegistrationKey string
	Name            string
	LocalSSHAddr    string
	IdleTimeout     time.Duration
	PingInterval    time.Duration
	Logger          *slog.Logger
	// HTTPClient overrides the handshake client. Production leaves it nil.
	HTTPClient *http.Client
	// SSHUser is the account whose authorized_keys receives paired keys.
	SSHUser string
	// SSHAdmin forces the Windows administrators_authorized_keys path when
	// non-nil. Nil detects membership of the Administrators group.
	SSHAdmin *bool
	// HomeDir overrides the home directory used for authorized_keys. Tests set it.
	HomeDir string
	// ProgramData overrides the Windows program data root. Empty uses the default.
	ProgramData string
	// EnableControl serves the loopback pair/unpair socket.
	EnableControl bool
	// ControlPath is the endpoint file. Empty uses the default next to the config.
	ControlPath string
	// ConfigPath is the agent config file, used to place the control file.
	ConfigPath string
}

// Run dials the relay and reconnects with exponential backoff until ctx ends.
func Run(ctx context.Context, cfg Config) error {
	cfg = cfg.normalized()
	if cfg.EnableControl {
		stop, err := startControl(ctx, cfg)
		if err != nil {
			cfg.logger().Info("local pairing control disabled", "err", err)
		} else {
			defer stop()
		}
	}
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	log := cfg.logger()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := time.Now()
		err := DialAndServe(ctx, cfg)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) >= time.Minute {
			backoff = time.Second
		}
		log.Info("relay connection closed", "err", err, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// DialAndServe maintains a single relay session and returns when it ends.
func DialAndServe(ctx context.Context, cfg Config) error {
	cfg = cfg.normalized()
	url, err := config.NormalizeRelayURL(cfg.RelayURL, "/agent")
	if err != nil {
		return err
	}
	ws, err := wsutil.DialHTTP(ctx, url, cfg.HTTPClient)
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	return serve(ctx, ws, cfg)
}

func (cfg Config) normalized() Config {
	if cfg.LocalSSHAddr == "" {
		cfg.LocalSSHAddr = "127.0.0.1:22"
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = protocol.DefaultIdleTimeout
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = protocol.PingInterval
	}
	return cfg
}

func (cfg Config) logger() *slog.Logger {
	if cfg.Logger != nil {
		return cfg.Logger
	}
	return slog.Default()
}

func serve(ctx context.Context, ws *websocket.Conn, cfg Config) error {
	cfg = cfg.normalized()
	log := cfg.logger()
	hello, err := protocol.MarshalHello(protocol.Hello{
		Role:     "agent",
		Key:      cfg.RegistrationKey,
		Name:     cfg.Name,
		Features: []string{protocol.FeaturePair},
	})
	if err != nil {
		return err
	}
	if err := wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypeHello, Payload: hello}); err != nil {
		return err
	}

	helloTimer := time.AfterFunc(10*time.Second, func() { ws.CloseNow() })
	f, err := wsutil.ReadFrame(ctx, ws)
	helloTimer.Stop()
	if err != nil {
		return err
	}
	switch f.Type {
	case protocol.TypeHelloOK:
	case protocol.TypeHelloErr:
		return errors.New("relay rejected registration: " + clip(f.Payload))
	default:
		return errors.New("unexpected relay handshake frame")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess := newSession(ctx, cancel, ws, cfg)
	defer sess.close()

	log.Info("connected to relay", "name", cfg.Name, "local", cfg.LocalSSHAddr)
	var last atomic.Int64
	wsutil.Touch(&last)
	go wsutil.Heartbeat(ctx, cfg.PingInterval, func() error {
		return sess.write(protocol.Frame{Type: protocol.TypePing})
	})
	go wsutil.WatchIdle(ctx, &last, cfg.IdleTimeout, func() {
		log.Info("relay idle timeout", "name", cfg.Name)
		sess.close()
	})

	for {
		f, err := wsutil.ReadFrame(ctx, ws)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		wsutil.Touch(&last)
		switch f.Type {
		case protocol.TypePing:
			_ = sess.write(protocol.Frame{Type: protocol.TypePong})
		case protocol.TypePong:
		case protocol.TypeOpen:
			// The payload is intentionally ignored. The relay must not be able
			// to choose where this agent dials.
			sess.onOpen(f.StreamID)
		case protocol.TypeData:
			sess.onData(f.StreamID, f.Payload)
		case protocol.TypeEnd:
			sess.onEnd(f.StreamID)
		case protocol.TypeClose:
			sess.onClose(f.StreamID)
		case protocol.TypePairInstall:
			if f.StreamID == 0 {
				sess.handlePair(f.Payload)
			}
		default:
		}
	}
}

type session struct {
	ctx    context.Context
	cancel context.CancelFunc
	ws     *websocket.Conn
	cfg    Config

	mu      sync.Mutex
	streams map[uint32]*slot
	closed  bool
	once    sync.Once
}

type slot struct {
	conn atomic.Value // net.Conn, set after dial succeeds
}

func newSession(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, cfg Config) *session {
	return &session{
		ctx:     ctx,
		cancel:  cancel,
		ws:      ws,
		cfg:     cfg,
		streams: make(map[uint32]*slot),
	}
}

func (s *session) close() {
	s.once.Do(func() {
		s.cancel()
		s.ws.CloseNow()
		s.mu.Lock()
		s.closed = true
		streams := s.streams
		s.streams = nil
		s.mu.Unlock()
		for _, sl := range streams {
			if c := slotConn(sl); c != nil {
				c.Close()
			}
		}
	})
}

func (s *session) write(f protocol.Frame) error {
	return wsutil.WriteFrame(s.ctx, s.ws, f)
}

func (s *session) onOpen(id uint32) {
	if id == 0 || !s.reserve(id) {
		reason := protocol.ReasonTooManyStreams
		if id == 0 {
			reason = "bad stream"
		}
		_ = s.write(protocol.Frame{
			Type:     protocol.TypeOpenErr,
			StreamID: id,
			Payload:  []byte(reason),
		})
		return
	}
	go s.dial(id)
}

// dial connects to the configured local SSH address and only that address.
func (s *session) dial(id uint32) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(s.ctx, "tcp", s.cfg.LocalSSHAddr)
	if err != nil {
		s.drop(id)
		_ = s.write(protocol.Frame{
			Type:     protocol.TypeOpenErr,
			StreamID: id,
			Payload:  []byte(protocol.ReasonDialFailed),
		})
		s.cfg.logger().Info("dial local ssh failed", "addr", s.cfg.LocalSSHAddr, "err", err)
		return
	}
	if !s.attach(id, conn) {
		conn.Close()
		_ = s.write(protocol.Frame{
			Type:     protocol.TypeOpenErr,
			StreamID: id,
			Payload:  []byte(protocol.ReasonAgentGone),
		})
		return
	}
	if err := s.write(protocol.Frame{Type: protocol.TypeOpenOK, StreamID: id}); err != nil {
		s.drop(id)
		conn.Close()
		return
	}
	go s.readLocal(id, conn)
}

func (s *session) reserve(id uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if _, exists := s.streams[id]; exists {
		return false
	}
	if len(s.streams) >= protocol.MaxStreams {
		return false
	}
	s.streams[id] = &slot{}
	return true
}

func (s *session) attach(id uint32, conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	sl, ok := s.streams[id]
	if !ok {
		return false
	}
	sl.conn.Store(conn)
	return true
}

func (s *session) drop(id uint32) {
	s.mu.Lock()
	sl := s.streams[id]
	delete(s.streams, id)
	s.mu.Unlock()
	if sl != nil {
		if c := slotConn(sl); c != nil {
			c.Close()
		}
	}
}

func (s *session) get(id uint32) *slot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[id]
}

func slotConn(sl *slot) net.Conn {
	if sl == nil {
		return nil
	}
	c, _ := sl.conn.Load().(net.Conn)
	return c
}

func (s *session) onData(id uint32, p []byte) {
	if len(p) == 0 {
		return
	}
	c := slotConn(s.get(id))
	if c == nil {
		return
	}
	if _, err := c.Write(p); err != nil {
		_ = s.write(protocol.Frame{Type: protocol.TypeClose, StreamID: id})
		s.drop(id)
	}
}

func (s *session) onEnd(id uint32) {
	c := slotConn(s.get(id))
	if c == nil {
		return
	}
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	c.Close()
	s.drop(id)
}

func (s *session) onClose(id uint32) {
	s.drop(id)
}

func (s *session) readLocal(id uint32, conn net.Conn) {
	buf := make([]byte, protocol.ChunkSize)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			if werr := s.write(protocol.Frame{
				Type:     protocol.TypeData,
				StreamID: id,
				Payload:  payload,
			}); werr != nil {
				s.drop(id)
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = s.write(protocol.Frame{Type: protocol.TypeEnd, StreamID: id})
				return
			}
			_ = s.write(protocol.Frame{Type: protocol.TypeClose, StreamID: id})
			s.drop(id)
			return
		}
	}
}

func clip(b []byte) string {
	if len(b) > 256 {
		b = b[:256]
	}
	return string(b)
}
