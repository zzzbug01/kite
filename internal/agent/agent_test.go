package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/zzzbug01/kite/internal/protocol"
	"github.com/zzzbug01/kite/internal/wsutil"
)

// TestIgnoresRelaySuppliedAddress proves the agent dials only LOCAL_SSH_ADDR,
// even when a relay puts a different address in the open frame.
func TestIgnoresRelaySuppliedAddress(t *testing.T) {
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close() })
	evil, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { evil.Close() })

	localHit := make(chan struct{}, 1)
	go func() {
		c, err := local.Accept()
		if err != nil {
			return
		}
		c.Close()
		localHit <- struct{}{}
	}()
	httpSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := wsutil.Accept(w, r)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		// Do not use r.Context() after the upgrade; it is cancelled on hijack.
		ctx := context.Background()
		f, err := wsutil.ReadFrame(ctx, ws)
		if err != nil || f.Type != protocol.TypeHello {
			return
		}
		if err := wsutil.WriteFrame(ctx, ws, protocol.Frame{Type: protocol.TypeHelloOK}); err != nil {
			return
		}
		payload := []byte(evil.Addr().String())
		if err := wsutil.WriteFrame(ctx, ws, protocol.Frame{
			Type:     protocol.TypeOpen,
			StreamID: 7,
			Payload:  payload,
		}); err != nil {
			return
		}
		// Drain until the agent hangs up so the test controls the lifetime.
		for {
			if _, err := wsutil.ReadFrame(ctx, ws); err != nil {
				return
			}
		}
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go httpSrv.Serve(ln)
	t.Cleanup(func() { httpSrv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- DialAndServe(ctx, Config{
			RelayURL:        "ws://" + ln.Addr().String() + "/agent",
			RegistrationKey: "test-key",
			Name:            "pc",
			LocalSSHAddr:    local.Addr().String(),
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	select {
	case <-localHit:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not dial LOCAL_SSH_ADDR")
	}

	if err := evil.(*net.TCPListener).SetDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if c, err := evil.Accept(); err == nil {
		c.Close()
		t.Fatalf("agent dialed %s from the open payload", evil.Addr())
	}
	cancel()
	select {
	case <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not exit")
	}
}
