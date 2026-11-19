package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zzzbug01/kite/internal/agent"
	"github.com/zzzbug01/kite/internal/client"
	"github.com/zzzbug01/kite/internal/clientkey"
	"github.com/zzzbug01/kite/internal/pairing"
	"github.com/zzzbug01/kite/internal/protocol"
	"github.com/zzzbug01/kite/internal/relay"
	"github.com/zzzbug01/kite/internal/wsutil"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startRelay(t *testing.T, idle, ping time.Duration) (*relay.Server, string) {
	t.Helper()
	return startRelayTLS(t, idle, ping, "", "")
}

func startRelayTLS(t *testing.T, idle, ping time.Duration, certFile, keyFile string) (*relay.Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := relay.New(relay.Config{
		RegistrationKey: "test-key",
		StateDir:        t.TempDir(),
		IdleTimeout:     idle,
		PingInterval:    ping,
		Logger:          discard(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- srv.Serve(ctx, ln, certFile, keyFile)
	}()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		select {
		case <-errc:
		case <-time.After(5 * time.Second):
			t.Errorf("relay did not stop")
		}
	})
	scheme := "ws"
	if certFile != "" {
		scheme = "wss"
	}
	return srv, scheme + "://" + ln.Addr().String()
}

func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func startMarker(t *testing.T, marker byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 32)
				if _, err := c.Read(buf); err != nil && !errors.Is(err, io.EOF) {
					return
				}
				_, _ = c.Write([]byte{marker})
			}(c)
		}
	}()
	return ln.Addr().String()
}

func startAgent(t *testing.T, base, name, local string, idle, ping time.Duration) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = agent.Run(ctx, agent.Config{
			RelayURL:        base + "/agent",
			RegistrationKey: "test-key",
			Name:            name,
			LocalSSHAddr:    local,
			IdleTimeout:     idle,
			PingInterval:    ping,
			Logger:          discard(),
		})
	}()
	t.Cleanup(cancel)
	return cancel
}

func waitAgent(t *testing.T, srv *relay.Server, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if srv.HasAgent(name) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent %s did not register", name)
}

func waitNoAgent(t *testing.T, srv *relay.Server, name string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !srv.HasAgent(name) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent %s still registered", name)
}

func dial(t *testing.T, base, name string) *client.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	var st *client.Stream
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err = client.Dial(ctx, client.Config{
			RelayURL:        base + "/client",
			RegistrationKey: "test-key",
			Agent:           name,
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          discard(),
		})
		if err == nil {
			t.Cleanup(func() { st.Close() })
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", name, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func roundTrip(t *testing.T, st *client.Stream, payload []byte) {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		_, err := st.Write(payload)
		if err == nil {
			err = st.CloseWrite()
		}
		errc <- err
	}()
	got, err := io.ReadAll(st)
	if werr := <-errc; werr != nil {
		t.Fatalf("write: %v", werr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestRoundTrip(t *testing.T) {
	srv, base := startRelay(t, time.Hour, time.Hour)
	local := startEcho(t)
	startAgent(t, base, "pc", local, time.Hour, time.Hour)
	waitAgent(t, srv, "pc")

	st := dial(t, base, "pc")
	payload := bytes.Repeat([]byte("kite"), 25_000) // 100 KiB, multiple frames
	roundTrip(t, st, payload)

	resp, err := http.Get("http://" + strings.TrimPrefix(base, "ws://") + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("ok")) {
		t.Fatalf("healthz %d %q", resp.StatusCode, body)
	}
}

func TestPipe(t *testing.T) {
	srv, base := startRelay(t, time.Hour, time.Hour)
	local := startEcho(t)
	startAgent(t, base, "pc", local, time.Hour, time.Hour)
	waitAgent(t, srv, "pc")
	st := dial(t, base, "pc")
	var out bytes.Buffer
	err := client.Pipe(context.Background(), st, bytes.NewReader([]byte("via-pipe")), &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "via-pipe" {
		t.Fatalf("pipe output %q", out.String())
	}
}

func TestConcurrentStreams(t *testing.T) {
	srv, base := startRelay(t, time.Hour, time.Hour)
	local := startEcho(t)
	startAgent(t, base, "pc", local, time.Hour, time.Hour)
	waitAgent(t, srv, "pc")

	var wg sync.WaitGroup
	errc := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			st, err := client.Dial(ctx, client.Config{
				RelayURL:        base + "/client",
				RegistrationKey: "test-key",
				Agent:           "pc",
				IdleTimeout:     time.Hour,
				PingInterval:    time.Hour,
				Logger:          discard(),
			})
			if err != nil {
				errc <- err
				return
			}
			defer st.Close()
			payload := bytes.Repeat([]byte{byte('a' + i)}, 4096)
			if _, err := st.Write(payload); err != nil {
				errc <- err
				return
			}
			if err := st.CloseWrite(); err != nil {
				errc <- err
				return
			}
			got, err := io.ReadAll(st)
			if err != nil {
				errc <- err
				return
			}
			if !bytes.Equal(got, payload) {
				errc <- errors.New("payload mismatch")
				return
			}
			errc <- nil
		}(i)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestStreamLimit(t *testing.T) {
	srv, base := startRelay(t, time.Hour, time.Hour)
	local := startEcho(t)
	startAgent(t, base, "pc", local, time.Hour, time.Hour)
	waitAgent(t, srv, "pc")

	var open []*client.Stream
	for i := 0; i < protocol.MaxStreams; i++ {
		open = append(open, dial(t, base, "pc"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Dial(ctx, client.Config{
		RelayURL:        base + "/client",
		RegistrationKey: "test-key",
		Agent:           "pc",
		IdleTimeout:     time.Hour,
		PingInterval:    time.Hour,
		Logger:          discard(),
	})
	if !errors.Is(err, client.ErrTooManyStreams) {
		t.Fatalf("65th stream: %v", err)
	}
	roundTrip(t, open[0], []byte("still-works"))
}

func TestRejectsBadKeyAndMissingAgent(t *testing.T) {
	srv, base := startRelay(t, time.Hour, time.Hour)
	local := startEcho(t)
	startAgent(t, base, "pc", local, time.Hour, time.Hour)
	waitAgent(t, srv, "pc")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Dial(ctx, client.Config{
		RelayURL:        base + "/client",
		RegistrationKey: "not-the-key",
		Agent:           "pc",
		Logger:          discard(),
	})
	if !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("bad key: %v", err)
	}
	if !srv.HasAgent("pc") {
		t.Fatal("rejected client cleared the agent")
	}

	_, err = client.Dial(ctx, client.Config{
		RelayURL:        base + "/client",
		RegistrationKey: "test-key",
		Agent:           "missing",
		Logger:          discard(),
	})
	if !errors.Is(err, client.ErrAgentNotFound) {
		t.Fatalf("missing agent: %v", err)
	}
}

func TestDialFailure(t *testing.T) {
	srv, base := startRelay(t, time.Hour, time.Hour)
	startAgent(t, base, "pc", "127.0.0.1:1", time.Hour, time.Hour)
	waitAgent(t, srv, "pc")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Dial(ctx, client.Config{
		RelayURL:        base + "/client",
		RegistrationKey: "test-key",
		Agent:           "pc",
		Logger:          discard(),
	})
	if err == nil || !strings.Contains(err.Error(), "dial") {
		t.Fatalf("expected dial failure, got %v", err)
	}
}

func TestReplaceAgent(t *testing.T) {
	srv, base := startRelay(t, time.Hour, time.Hour)
	addrA := startMarker(t, 'A')
	addrB := startMarker(t, 'B')

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	doneA := make(chan error, 1)
	go func() {
		doneA <- agent.DialAndServe(ctxA, agent.Config{
			RelayURL:        base + "/agent",
			RegistrationKey: "test-key",
			Name:            "pc",
			LocalSSHAddr:    addrA,
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          discard(),
		})
	}()
	waitAgent(t, srv, "pc")

	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	go func() {
		_ = agent.DialAndServe(ctxB, agent.Config{
			RelayURL:        base + "/agent",
			RegistrationKey: "test-key",
			Name:            "pc",
			LocalSSHAddr:    addrB,
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          discard(),
		})
	}()

	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("old agent was not replaced")
	}
	waitAgent(t, srv, "pc")

	st := dial(t, base, "pc")
	if _, err := st.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(st, buf); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 'B' {
		t.Fatalf("reached agent %q, want B", buf)
	}
}

func TestIdleDropsSilentPeer(t *testing.T) {
	srv, base := startRelay(t, 800*time.Millisecond, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, err := wsutil.Dial(ctx, base+"/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	hello, err := protocol.MarshalHello(protocol.Hello{Role: "agent", Key: "test-key", Name: "silent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypeHello, Payload: hello}); err != nil {
		t.Fatal(err)
	}
	f, err := wsutil.ReadFrame(ctx, ws)
	if err != nil || f.Type != protocol.TypeHelloOK {
		t.Fatalf("hello ok: %v %+v", err, f)
	}
	waitAgent(t, srv, "silent")
	if _, err := wsutil.ReadFrame(ctx, ws); err == nil {
		t.Fatal("expected the relay to drop the silent connection")
	}
	waitNoAgent(t, srv, "silent")
}

func TestHeartbeatKeepsConnection(t *testing.T) {
	idle := 1500 * time.Millisecond
	ping := 200 * time.Millisecond
	srv, base := startRelay(t, idle, ping)
	local := startEcho(t)
	startAgent(t, base, "pc", local, idle, ping)
	waitAgent(t, srv, "pc")
	time.Sleep(4 * time.Second)
	if !srv.HasAgent("pc") {
		t.Fatal("application heartbeat did not keep the agent connected")
	}
	st := dial(t, base, "pc")
	roundTrip(t, st, []byte("after-idle-window"))
}

func TestOpenFrameCarriesNoAddress(t *testing.T) {
	_, base := startRelay(t, time.Hour, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, err := wsutil.Dial(ctx, base+"/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	hello, _ := protocol.MarshalHello(protocol.Hello{Role: "agent", Key: "test-key", Name: "pc"})
	if err := wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypeHello, Payload: hello}); err != nil {
		t.Fatal(err)
	}
	if _, err := wsutil.ReadFrame(ctx, ws); err != nil {
		t.Fatal(err)
	}

	got := make(chan protocol.Frame, 1)
	go func() {
		for {
			f, err := wsutil.ReadFrame(ctx, ws)
			if err != nil {
				return
			}
			if f.Type == protocol.TypePing {
				_ = wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypePong})
				continue
			}
			if f.Type == protocol.TypeOpen {
				got <- f
				_ = wsutil.WriteFrame(ctx, ws, protocol.Frame{
					Type:     protocol.TypeOpenErr,
					StreamID: f.StreamID,
					Payload:  []byte(protocol.ReasonDialFailed),
				})
				return
			}
		}
	}()

	_, err = client.Dial(ctx, client.Config{
		RelayURL:        base + "/client",
		RegistrationKey: "test-key",
		Agent:           "pc",
		Logger:          discard(),
	})
	if err == nil {
		t.Fatal("expected open to fail")
	}
	select {
	case f := <-got:
		if len(f.Payload) != 0 {
			t.Fatalf("open payload must be empty, got %q", f.Payload)
		}
		if f.StreamID == 0 {
			t.Fatal("stream id 0")
		}
	case <-ctx.Done():
		t.Fatal("agent did not observe an open frame")
	}
}

func TestTLSRelay(t *testing.T) {
	cert, key := writeCert(t)
	srv, base := startRelayTLS(t, time.Hour, time.Hour, cert, key)
	local := startEcho(t)
	// Ephemeral test certificate generated in a temp directory. It is not a
	// credential and is never written into the repository.
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = agent.Run(ctx, agent.Config{
			RelayURL:        base + "/agent",
			RegistrationKey: "test-key",
			Name:            "pc",
			LocalSSHAddr:    local,
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          discard(),
			HTTPClient:      hc,
		})
	}()

	opCtx, opCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer opCancel()
	deadline := time.Now().Add(5 * time.Second)
	var st *client.Stream
	var err error
	for {
		st, err = client.Dial(opCtx, client.Config{
			RelayURL:        base + "/client",
			RegistrationKey: "test-key",
			Agent:           "pc",
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          discard(),
			HTTPClient:      hc,
		})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer st.Close()
	if !srv.HasAgent("pc") {
		t.Fatal("agent missing")
	}
	roundTrip(t, st, []byte("tls-ok"))
}

func TestPairingAndClientToken(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	srv := relay.New(relay.Config{
		RegistrationKey: "test-key",
		PreviousKey:     "old-key",
		StateDir:        state,
		IdleTimeout:     time.Hour,
		PingInterval:    time.Hour,
		Logger:          logger,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ctx, ln, "", "") }()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		select {
		case <-errc:
		case <-time.After(5 * time.Second):
			t.Errorf("relay did not stop")
		}
	})
	base := "ws://" + ln.Addr().String()
	home := t.TempDir()
	local := startEcho(t)
	admin := false
	sshUser := "tester"
	if runtime.GOOS == "windows" {
		// icacls rejects an account that does not exist on this machine.
		u, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		sshUser = u.Username
		if i := strings.LastIndexAny(sshUser, `\/`); i >= 0 {
			sshUser = sshUser[i+1:]
		}
	}
	go func() {
		_ = agent.Run(ctx, agent.Config{
			RelayURL:        base + "/agent",
			RegistrationKey: "test-key",
			Name:            "pc",
			LocalSSHAddr:    local,
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          discard(),
			SSHUser:         sshUser,
			HomeDir:         home,
			SSHAdmin:        &admin,
		})
	}()
	waitAgent(t, srv, "pc")

	httpBase := "http://" + ln.Addr().String()
	created, err := pairing.PostCreate(ctx, httpBase, pairing.CreateRequest{
		Key:       "test-key",
		Agent:     "pc",
		SSHUser:   sshUser,
		ClientURL: base + "/client",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.PostCreate(ctx, httpBase, pairing.CreateRequest{
		Key: "not-the-key", Agent: "pc", ClientURL: base + "/client",
	}); err == nil {
		t.Fatal("bad registration key was accepted")
	}

	ws, err := wsutil.Dial(ctx, base+"/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	oldHello, err := protocol.MarshalHello(protocol.Hello{Role: "agent", Key: "test-key", Name: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypeHello, Payload: oldHello}); err != nil {
		t.Fatal(err)
	}
	if _, err := wsutil.ReadFrame(ctx, ws); err != nil {
		t.Fatal(err)
	}
	waitAgent(t, srv, "old")
	if _, err := pairing.PostCreate(ctx, httpBase, pairing.CreateRequest{
		Key: "test-key", Agent: "old", ClientURL: base + "/client",
	}); err == nil || !strings.Contains(err.Error(), "does not support pairing") {
		t.Fatalf("old agent: %v", err)
	}

	_, pub, err := clientkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	redeemed, err := pairing.PostRedeem(ctx, httpBase, pairing.RedeemRequest{
		Code: created.Code, PublicKey: pub, Label: "laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if redeemed.Agent != "pc" || redeemed.SSHUser != sshUser || !strings.HasPrefix(redeemed.ClientToken, "kite_") {
		t.Fatalf("%+v", redeemed)
	}
	if _, err := pairing.PostRedeem(ctx, httpBase, pairing.RedeemRequest{
		Code: created.Code, PublicKey: pub, Label: "laptop",
	}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("reuse: %v", err)
	}
	keys, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(keys), pub) || !strings.Contains(string(keys), "kite-pair laptop") {
		t.Fatalf("authorized_keys:\n%s", keys)
	}
	logText := logs.String()
	for _, secret := range []string{"test-key", "old-key", created.Code, strings.ReplaceAll(created.Code, "-", ""), redeemed.ClientToken} {
		if strings.Contains(logText, secret) {
			t.Fatalf("log contains secret %q\n%s", secret, logText)
		}
	}

	st, err := client.Dial(ctx, client.Config{
		RelayURL:        base + "/client",
		RegistrationKey: redeemed.ClientToken,
		Auth:            "client",
		Agent:           "pc",
		IdleTimeout:     time.Hour,
		PingInterval:    time.Hour,
		Logger:          discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, st, []byte("paired"))
	st.Close()

	for _, key := range []string{"test-key", "old-key"} {
		legacy, err := client.Dial(ctx, client.Config{
			RelayURL:        base + "/client",
			RegistrationKey: key,
			Agent:           "pc",
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          discard(),
		})
		if err != nil {
			t.Fatalf("legacy %s: %v", key, err)
		}
		roundTrip(t, legacy, []byte("legacy"))
		legacy.Close()
	}

	if _, err := pairing.Open(state).Revoke("laptop"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Dial(ctx, client.Config{
		RelayURL:        base + "/client",
		RegistrationKey: redeemed.ClientToken,
		Auth:            "client",
		Agent:           "pc",
		IdleTimeout:     time.Hour,
		PingInterval:    time.Hour,
		Logger:          discard(),
	}); !errors.Is(err, client.ErrUnauthorized) && (err == nil || !strings.Contains(err.Error(), "unauthorized")) {
		t.Fatalf("revoked token: %v", err)
	}
}

func writeCert(t *testing.T) (string, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
