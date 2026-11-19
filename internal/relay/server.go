// Package relay is the public kite-relay: it accepts agent and client
// WebSockets, checks the shared registration key, and splices client streams
// onto the matching agent connection.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/zzzbug01/kite/internal/auth"
	"github.com/zzzbug01/kite/internal/invite"
	"github.com/zzzbug01/kite/internal/pairing"
	"github.com/zzzbug01/kite/internal/protocol"
	"github.com/zzzbug01/kite/internal/wsutil"
)

const openTimeout = 15 * time.Second

// Config controls a relay server.
type Config struct {
	RegistrationKey string
	// PreviousKey is accepted in addition to RegistrationKey so an operator
	// can rotate the agent secret without a flag day. Empty disables it.
	PreviousKey string
	// StateDir holds hashed per-client tokens. Empty uses pairing.DefaultStateDir.
	StateDir    string
	IdleTimeout time.Duration
	// PingInterval defaults to 20s. Tests may shorten it.
	PingInterval time.Duration
	// PairTTL is the default pairing-code lifetime. Zero means 10 minutes.
	PairTTL time.Duration
	// TrustedProxies nil means loopback only. An empty non-nil slice trusts
	// nobody. Otherwise these networks may supply X-Forwarded-For.
	TrustedProxies []*net.IPNet
	// HandshakeTimeout bounds the wait for the first hello on /agent and
	// /client. Zero means 10s. IdleTimeout does not apply until auth succeeds.
	HandshakeTimeout time.Duration
	Logger           *slog.Logger
}

// Server is an HTTP server that speaks the kite protocol on /agent and /client.
type Server struct {
	key  string
	prev string
	idle time.Duration
	ping time.Duration
	log  *slog.Logger

	tokens    *pairing.Store
	invites   *invite.Store
	inviteLim *pairing.Limiter
	broker    *pairing.Broker
	trusted   []*net.IPNet
	handshake time.Duration

	mu     sync.Mutex
	agents map[string]*agentConn

	httpSrv *http.Server

	rootCtx    context.Context
	rootCancel context.CancelFunc
}

// New builds a server. Call Serve to listen.
func New(cfg Config) *Server {
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = protocol.DefaultIdleTimeout
	}
	ping := cfg.PingInterval
	if ping <= 0 {
		ping = protocol.PingInterval
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	handshake := cfg.HandshakeTimeout
	if handshake <= 0 {
		handshake = protocol.HandshakeTimeout
	}
	s := &Server{
		key:       cfg.RegistrationKey,
		prev:      cfg.PreviousKey,
		idle:      idle,
		ping:      ping,
		log:       log,
		tokens:    pairing.Open(cfg.StateDir),
		invites:   invite.Open(cfg.StateDir),
		inviteLim: pairing.NewLimiter(),
		broker:    pairing.NewBroker(pairing.Config{TTL: cfg.PairTTL}),
		trusted:   cfg.TrustedProxies,
		handshake: handshake,
		agents:    make(map[string]*agentConn),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/agent", s.handleAgent)
	mux.HandleFunc("/client", s.handleClient)
	mux.HandleFunc("POST /pair", s.handlePairCreate)
	mux.HandleFunc("POST /pair/redeem", s.handlePairRedeem)
	mux.HandleFunc("POST /invite/redeem", s.handleInviteRedeem)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	return s
}

// Handler returns the HTTP handler, useful for tests that supply their own server.
func (s *Server) Handler() http.Handler {
	return s.httpSrv.Handler
}

// HasAgent reports whether name is currently registered.
func (s *Server) HasAgent(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[name]
	return ok && a != nil
}

// Serve accepts connections until ctx is cancelled. A nil cert/key pair serves
// plain WebSocket; both set serves TLS. A TLS listener is what you want only
// when the relay terminates TLS itself. The normal deployment is plain WS
// behind nginx or Caddy.
func (s *Server) Serve(ctx context.Context, ln net.Listener, certFile, keyFile string) error {
	if err := s.tokens.Ensure(); err != nil {
		return fmt.Errorf("STATE_DIR %s: %w", s.tokens.Dir(), err)
	}
	ctx, cancel := context.WithCancel(ctx)
	s.rootCtx = ctx
	s.rootCancel = cancel
	defer cancel()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		agents := make([]*agentConn, 0, len(s.agents))
		for _, a := range s.agents {
			agents = append(agents, a)
		}
		s.mu.Unlock()
		for _, a := range agents {
			a.close()
		}
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = s.httpSrv.Shutdown(shutCtx)
	}()

	var err error
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return errors.New("TLS_CERT_FILE and TLS_KEY_FILE must both be set")
		}
		err = s.httpSrv.ServeTLS(ln, certFile, keyFile)
	} else {
		err = s.httpSrv.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	ws, err := wsutil.Accept(w, r)
	if err != nil {
		s.log.Info("agent upgrade failed", "err", err, "remote", r.RemoteAddr)
		return
	}
	defer ws.CloseNow()

	parent := s.rootCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	hello, ok := s.readHello(ctx, ws, "agent")
	if !ok {
		return
	}
	if hello.Role != "agent" || protocol.CheckAgentName(hello.Name) != nil {
		s.reject(ctx, ws, protocol.ReasonBadHello)
		return
	}

	a := newAgentConn(s, hello.Name, ws, ctx, cancel)
	for _, feature := range hello.Features {
		a.features[feature] = true
	}
	s.register(a)
	defer s.unregister(a)
	defer a.close()

	if err := a.write(protocol.Frame{Type: protocol.TypeHelloOK}); err != nil {
		return
	}
	s.log.Info("agent registered", "name", a.name, "remote", r.RemoteAddr)

	wsutil.Touch(&a.lastRecv)
	go wsutil.Heartbeat(ctx, s.ping, func() error {
		return a.write(protocol.Frame{Type: protocol.TypePing})
	})
	go wsutil.WatchIdle(ctx, &a.lastRecv, s.idle, func() {
		s.log.Info("agent idle timeout", "name", a.name)
		a.close()
	})

	a.readLoop()
}

func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	ws, err := wsutil.Accept(w, r)
	if err != nil {
		s.log.Info("client upgrade failed", "err", err, "remote", r.RemoteAddr)
		return
	}
	defer ws.CloseNow()

	parent := s.rootCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	hello, ok := s.readHello(ctx, ws, "client")
	if !ok {
		return
	}
	if hello.Role != "client" || !protocol.ValidName(hello.Agent) {
		s.reject(ctx, ws, protocol.ReasonBadHello)
		return
	}

	agent := s.lookup(hello.Agent)
	if agent == nil {
		s.reject(ctx, ws, protocol.ReasonAgentNotFound)
		return
	}

	st, err := agent.alloc(ws, ctx, cancel)
	if err != nil {
		s.reject(ctx, ws, protocol.ReasonTooManyStreams)
		return
	}
	defer agent.remove(st.id)
	defer st.finish()

	if err := agent.write(protocol.Frame{Type: protocol.TypeOpen, StreamID: st.id}); err != nil {
		s.reject(ctx, ws, protocol.ReasonAgentGone)
		return
	}

	var last atomic.Int64
	wsutil.Touch(&last)
	go wsutil.Heartbeat(ctx, s.ping, func() error {
		return wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypePing})
	})
	go wsutil.WatchIdle(ctx, &last, s.idle, func() {
		cancel()
		ws.CloseNow()
	})
	go s.readClient(ctx, ws, agent, st, &last)

	timer := time.NewTimer(openTimeout)
	defer timer.Stop()
	select {
	case err := <-st.opened:
		if err != nil {
			_ = wsutil.WriteFrame(ctx, ws, protocol.Frame{
				Type:     protocol.TypeOpenErr,
				StreamID: st.id,
				Payload:  []byte(clipReason(err.Error())),
			})
			return
		}
		if err := st.finishHandshake(); err != nil {
			return
		}
	case <-timer.C:
		s.reject(ctx, ws, protocol.ReasonOpenTimeout)
		_ = agent.write(protocol.Frame{Type: protocol.TypeClose, StreamID: st.id})
		return
	case <-ctx.Done():
		_ = agent.write(protocol.Frame{Type: protocol.TypeClose, StreamID: st.id})
		return
	case <-agent.done:
		s.reject(ctx, ws, protocol.ReasonAgentGone)
		return
	}

	select {
	case <-st.done:
	case <-ctx.Done():
	case <-agent.done:
	}
	_ = agent.write(protocol.Frame{Type: protocol.TypeClose, StreamID: st.id})
}

// readHello reads the first frame and checks the registration key.
// role is only used so a dummy compare still runs when the frame is unusable.
func (s *Server) readHello(ctx context.Context, ws *websocket.Conn, _ string) (protocol.Hello, bool) {
	// The idle watcher starts only after a successful hello. Cancelling the
	// read context closes the websocket in this library, which would drop the
	// error frame, so the timer writes handshake timeout and then cancels.
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	finished := false
	timer := time.AfterFunc(s.handshake, func() {
		mu.Lock()
		if finished {
			mu.Unlock()
			return
		}
		finished = true
		mu.Unlock()
		s.log.Info("handshake timeout", "limit", s.handshake.String())
		s.reject(ctx, ws, protocol.ReasonHandshakeTimeout)
		cancel()
	})
	defer timer.Stop()
	f, err := wsutil.ReadFrame(hctx, ws)
	mu.Lock()
	finished = true
	mu.Unlock()
	if err != nil {
		return protocol.Hello{}, false
	}
	if f.Type != protocol.TypeHello {
		// Compare anyway so a wrong frame type is not a cheaper reject than a bad key.
		_ = auth.Equal("", s.key)
		s.reject(ctx, ws, protocol.ReasonBadHello)
		return protocol.Hello{}, false
	}
	h, err := protocol.UnmarshalHello(f.Payload)
	if err != nil {
		_ = auth.Equal("", s.key)
		s.reject(ctx, ws, protocol.ReasonBadHello)
		return protocol.Hello{}, false
	}
	if reason := s.authorize(&h); reason != "" {
		s.reject(ctx, ws, reason)
		return protocol.Hello{}, false
	}
	// Drop the secret before the value escapes this function's success path
	// into logs. Callers only need role, name, and features.
	h.Key = ""
	return h, true
}

// authorize checks a registration key, a per-agent token, or a per-client
// token. An empty Auth field is the v0.1.0-rc.2 registration-key handshake.
func (s *Server) authorize(h *protocol.Hello) string {
	switch h.Auth {
	case "", "registration":
		if !s.registrationOK(h.Key) {
			return protocol.ReasonUnauthorized
		}
	case "agent":
		if h.Role != "agent" || protocol.CheckAgentName(h.Name) != nil || !s.invites.Authenticate(h.Key, h.Name) {
			return protocol.ReasonUnauthorized
		}
	case "client":
		if h.Role != "client" || !protocol.ValidName(h.Agent) {
			return protocol.ReasonUnauthorized
		}
		if _, ok := s.tokens.Authenticate(h.Key, h.Agent); !ok {
			return protocol.ReasonUnauthorized
		}
	default:
		return "unsupported credential; this relay accepts a registration key, an agent token, or a paired client token"
	}
	return ""
}

// agentCredential accepts the shared registration key, or an agent token
// when auth is "agent". Both compares run so a token is not a cheaper
// reject than a wrong registration key.
func (s *Server) agentCredential(auth, key, name string) bool {
	reg := s.registrationOK(key)
	token := s.invites.Authenticate(key, name)
	switch auth {
	case "agent":
		return token
	case "", "registration":
		return reg
	default:
		return false
	}
}

// registrationOK accepts the current key and, during rotation, the previous
// key. Both compares always run. An empty previous key does not match.
func (s *Server) registrationOK(got string) bool {
	ok := auth.Equal(got, s.key) && s.key != ""
	prevOK := auth.Equal(got, s.prev) && s.prev != ""
	return ok || prevOK
}

func (s *Server) reject(ctx context.Context, ws *websocket.Conn, reason string) {
	_ = wsutil.WriteFrame(ctx, ws, protocol.Frame{
		Type:    protocol.TypeHelloErr,
		Payload: []byte(reason),
	})
	s.log.Info("rejected connection", "reason", reason)
}

func (s *Server) register(a *agentConn) {
	s.mu.Lock()
	old := s.agents[a.name]
	s.agents[a.name] = a
	s.mu.Unlock()
	if old != nil && old != a {
		s.log.Info("replacing agent", "name", a.name)
		old.close()
	}
}

func (s *Server) unregister(a *agentConn) {
	s.mu.Lock()
	if cur, ok := s.agents[a.name]; ok && cur == a {
		delete(s.agents, a.name)
	}
	s.mu.Unlock()
}

func (s *Server) lookup(name string) *agentConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents[name]
}

func (s *Server) readClient(ctx context.Context, ws *websocket.Conn, agent *agentConn, st *stream, last *atomic.Int64) {
	defer st.finish()
	for {
		f, err := wsutil.ReadFrame(ctx, ws)
		if err != nil {
			return
		}
		wsutil.Touch(last)
		switch f.Type {
		case protocol.TypePing:
			_ = wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypePong})
		case protocol.TypePong:
		case protocol.TypeData, protocol.TypeEnd, protocol.TypeClose:
			if f.StreamID != 0 && f.StreamID != st.id {
				continue
			}
			out := protocol.Frame{Type: f.Type, StreamID: st.id, Payload: append([]byte(nil), f.Payload...)}
			if !st.ready.Load() {
				// Client bytes before open_ok are queued inside forwardFromClient
				// only once the stream is live. Drop anything earlier except close.
				if f.Type == protocol.TypeClose {
					return
				}
				continue
			}
			if err := agent.write(out); err != nil {
				return
			}
			if f.Type == protocol.TypeClose {
				return
			}
		default:
		}
	}
}

type agentConn struct {
	srv  *Server
	name string
	ws   *websocket.Conn

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	mu      sync.Mutex
	streams map[uint32]*stream
	nextID  uint32
	closed  bool

	features map[string]bool
	pairMu   sync.Mutex
	pairWait map[string]chan protocol.PairResult

	lastRecv atomic.Int64
}

func newAgentConn(srv *Server, name string, ws *websocket.Conn, ctx context.Context, cancel context.CancelFunc) *agentConn {
	return &agentConn{
		srv:      srv,
		name:     name,
		ws:       ws,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		streams:  make(map[uint32]*stream),
		features: make(map[string]bool),
		pairWait: make(map[string]chan protocol.PairResult),
	}
}

func (a *agentConn) close() {
	a.once.Do(func() {
		a.cancel()
		close(a.done)
		a.ws.CloseNow()
		a.mu.Lock()
		a.closed = true
		streams := a.streams
		a.streams = nil
		a.mu.Unlock()
		for _, st := range streams {
			st.client.CloseNow()
			st.failOpen(errors.New(protocol.ReasonAgentGone))
			st.finish()
		}
	})
}

func (a *agentConn) write(f protocol.Frame) error {
	return wsutil.WriteFrame(a.ctx, a.ws, f)
}

func (a *agentConn) alloc(client *websocket.Conn, ctx context.Context, cancel context.CancelFunc) (*stream, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New(protocol.ReasonAgentGone)
	}
	if len(a.streams) >= protocol.MaxStreams {
		return nil, errors.New(protocol.ReasonTooManyStreams)
	}
	for {
		a.nextID++
		if a.nextID == 0 {
			a.nextID = 1
		}
		if _, exists := a.streams[a.nextID]; !exists {
			break
		}
	}
	st := newStream(a.nextID, client, ctx, cancel)
	a.streams[a.nextID] = st
	return st, nil
}

func (a *agentConn) remove(id uint32) {
	a.mu.Lock()
	delete(a.streams, id)
	a.mu.Unlock()
}

func (a *agentConn) get(id uint32) *stream {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.streams[id]
}

func (a *agentConn) readLoop() {
	for {
		f, err := wsutil.ReadFrame(a.ctx, a.ws)
		if err != nil {
			return
		}
		wsutil.Touch(&a.lastRecv)
		switch f.Type {
		case protocol.TypePing:
			_ = a.write(protocol.Frame{Type: protocol.TypePong})
		case protocol.TypePong:
		case protocol.TypePairResult:
			var res protocol.PairResult
			if json.Unmarshal(f.Payload, &res) == nil && res.RequestID != "" {
				a.deliverPair(res)
			}
		case protocol.TypeOpenOK:
			if st := a.get(f.StreamID); st != nil {
				st.failOpen(nil)
			}
		case protocol.TypeOpenErr:
			if st := a.get(f.StreamID); st != nil {
				msg := protocol.ReasonOpenFailed
				if len(f.Payload) > 0 {
					msg = clipReason(string(f.Payload))
				}
				// Signal only. finish() cancels the client handler context, and
				// the handler still needs that context to write the error frame.
				st.failOpen(errors.New(msg))
				a.remove(st.id)
			}
		case protocol.TypeData, protocol.TypeEnd, protocol.TypeClose:
			st := a.get(f.StreamID)
			if st == nil {
				continue
			}
			// A close before open_ok must wake the client waiter. Do not
			// finish() yet: that cancels the handler before it can write the error.
			if f.Type == protocol.TypeClose && !st.ready.Load() {
				st.failOpen(errors.New(protocol.ReasonOpenFailed))
				a.remove(st.id)
				continue
			}
			st.forwardToClient(protocol.Frame{
				Type:     f.Type,
				StreamID: st.id,
				Payload:  append([]byte(nil), f.Payload...),
			})
			if f.Type == protocol.TypeClose {
				a.remove(st.id)
				st.finish()
			}
		default:
		}
	}
}

// stream links one client WebSocket to one stream id on an agent connection.
type stream struct {
	id     uint32
	client *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	opened chan error
	done   chan struct{}
	once   sync.Once

	ready atomic.Bool

	mu            sync.Mutex
	handshakeDone bool
	pending       []protocol.Frame
	openSignaled  bool
}

func newStream(id uint32, client *websocket.Conn, ctx context.Context, cancel context.CancelFunc) *stream {
	return &stream{
		id:     id,
		client: client,
		ctx:    ctx,
		cancel: cancel,
		opened: make(chan error, 1),
		done:   make(chan struct{}),
	}
}

func (st *stream) failOpen(err error) {
	st.mu.Lock()
	if st.openSignaled {
		st.mu.Unlock()
		return
	}
	st.openSignaled = true
	st.mu.Unlock()
	st.opened <- err
}

func (st *stream) finish() {
	st.once.Do(func() {
		close(st.done)
		st.cancel()
	})
}

// finishHandshake writes open_ok to the client, then any frames from the agent
// that arrived while the client was still waiting. Holding mu across those
// writes keeps them in order.
//
// ready is set before open_ok is written. The client is allowed to send SSH
// bytes as soon as it observes open_ok, and that can happen before WriteFrame
// returns to us. Marking the stream ready first means those bytes are not dropped.
func (st *stream) finishHandshake() error {
	st.ready.Store(true)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := wsutil.WriteFrame(st.ctx, st.client, protocol.Frame{
		Type:     protocol.TypeOpenOK,
		StreamID: st.id,
	}); err != nil {
		return err
	}
	st.handshakeDone = true
	pending := st.pending
	st.pending = nil
	for _, f := range pending {
		if err := wsutil.WriteFrame(st.ctx, st.client, f); err != nil {
			return err
		}
	}
	return nil
}

func (st *stream) forwardToClient(f protocol.Frame) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.handshakeDone {
		if len(st.pending) >= 64 {
			st.finish()
			return
		}
		st.pending = append(st.pending, f)
		return
	}
	if err := wsutil.WriteFrame(st.ctx, st.client, f); err != nil {
		st.finish()
		st.client.CloseNow()
	}
}

func clipReason(s string) string {
	if len(s) > 256 {
		return s[:256]
	}
	return s
}
