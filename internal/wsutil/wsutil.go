// Package wsutil wraps WebSocket reads and writes for kite frames.
package wsutil

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/zzzbug01/kite/internal/protocol"
)

// WriteTimeout bounds a single frame write. A stuck peer must not pin a
// connection forever; the idle watcher is a backstop, this is the fast path.
const WriteTimeout = 30 * time.Second

// Accept upgrades an HTTP request. Origin checks are disabled: kite is not a
// browser app, and the registration key is what authorizes a connection.
func Accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(protocol.MaxMessage)
	return c, nil
}

// Dial opens a WebSocket. ctx bounds the handshake only; cancelling it after
// Dial returns does not close the connection.
func Dial(ctx context.Context, url string) (*websocket.Conn, error) {
	return DialHTTP(ctx, url, nil)
}

// DialHTTP is Dial with an optional HTTP client. Tests use it to trust an
// ephemeral certificate. A nil client uses the default transport.
func DialHTTP(ctx context.Context, url string, hc *http.Client) (*websocket.Conn, error) {
	opts := &websocket.DialOptions{CompressionMode: websocket.CompressionDisabled}
	if hc != nil {
		opts.HTTPClient = hc
	}
	c, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(protocol.MaxMessage)
	return c, nil
}

// ReadFrame reads one binary kite frame.
func ReadFrame(ctx context.Context, c *websocket.Conn) (protocol.Frame, error) {
	typ, data, err := c.Read(ctx)
	if err != nil {
		return protocol.Frame{}, err
	}
	if typ != websocket.MessageBinary {
		return protocol.Frame{}, errors.New("expected binary websocket message")
	}
	return protocol.Decode(data)
}

// WriteFrame writes one kite frame. A 30s timeout closes a stuck connection
// through the websocket library's write context.
func WriteFrame(ctx context.Context, c *websocket.Conn, f protocol.Frame) error {
	b, err := protocol.Encode(f)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	return c.Write(wctx, websocket.MessageBinary, b)
}

// Touch records that traffic was received.
func Touch(last *atomic.Int64) {
	last.Store(time.Now().UnixNano())
}

// Heartbeat sends an application ping every interval until ctx is done or
// a write fails. Outgoing pings are not treated as received traffic; the peer
// must answer (or otherwise send a frame) to keep the idle timer armed.
func Heartbeat(ctx context.Context, interval time.Duration, send func() error) {
	if interval <= 0 {
		interval = protocol.PingInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := send(); err != nil {
				return
			}
		}
	}
}

// WatchIdle calls onTimeout when nothing has been received for timeout.
// last must be updated on every received frame, not on sends.
func WatchIdle(ctx context.Context, last *atomic.Int64, timeout time.Duration, onTimeout func()) {
	if timeout <= 0 {
		timeout = protocol.DefaultIdleTimeout
	}
	tick := timeout / 3
	if tick < 20*time.Millisecond {
		tick = 20 * time.Millisecond
	}
	if tick > 5*time.Second {
		tick = 5 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			seen := last.Load()
			if seen == 0 {
				continue
			}
			if time.Since(time.Unix(0, seen)) > timeout {
				onTimeout()
				return
			}
		}
	}
}
