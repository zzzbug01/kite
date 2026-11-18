package relay

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/zzzbug01/kite/internal/protocol"
)

func TestHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		RegistrationKey:  "test-key",
		StateDir:         t.TempDir(),
		IdleTimeout:      time.Hour,
		PingInterval:     time.Hour,
		HandshakeTimeout: 200 * time.Millisecond,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln, "", "") }()

	for _, path := range []string{"/agent", "/client"} {
		t.Run(path, func(t *testing.T) {
			dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer dcancel()
			ws, _, err := websocket.Dial(dctx, "ws://"+ln.Addr().String()+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.CloseNow()
			start := time.Now()
			typ, data, err := ws.Read(dctx)
			elapsed := time.Since(start)
			if elapsed > 2*time.Second {
				t.Fatalf("still open after %s (idle timeout must not be what closes this)", elapsed)
			}
			if err != nil {
				t.Fatalf("expected handshake timeout frame, got %v after %s", err, elapsed)
			}
			if typ != websocket.MessageBinary {
				t.Fatalf("type %v", typ)
			}
			f, err := protocol.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if f.Type != protocol.TypeHelloErr || string(f.Payload) != protocol.ReasonHandshakeTimeout {
				t.Fatalf("frame type %d payload %q", f.Type, f.Payload)
			}
		})
	}
}

func TestClientIPUsesRightmostForwarded(t *testing.T) {
	// The left-most value is whatever the client claimed. nginx's
	// $proxy_add_x_forwarded_for appends the address it saw.
	got := ClientIP("127.0.0.1:8080", "1.2.3.4, 203.0.113.9", nil)
	if got != "203.0.113.9" {
		t.Fatal(got)
	}
	if got := ClientIP("198.51.100.2:443", "1.2.3.4, 203.0.113.9", nil); got != "198.51.100.2" {
		t.Fatalf("untrusted peer used the header: %s", got)
	}
	none, err := ParseTrustedProxies("none")
	if err != nil {
		t.Fatal(err)
	}
	if got := ClientIP("127.0.0.1:9", "203.0.113.9", none); got != "127.0.0.1" {
		t.Fatal(got)
	}
	extra, err := ParseTrustedProxies("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	if got := ClientIP("10.1.2.3:80", "1.2.3.4, 203.0.113.50", extra); got != "203.0.113.50" {
		t.Fatal(got)
	}
}
