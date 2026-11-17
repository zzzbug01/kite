package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestPingPongTypes(t *testing.T) {
	if TypePing != 10 || TypePong != 11 {
		t.Fatalf("heartbeat types changed: ping=%d pong=%d", TypePing, TypePong)
	}
	if HeaderSize != 5 || MaxPayload != 256*1024 || MaxStreams != 64 {
		t.Fatalf("frame limits changed: header=%d payload=%d streams=%d", HeaderSize, MaxPayload, MaxStreams)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("abc"), 1000)
	in := Frame{Type: TypeData, StreamID: 0x01020304, Payload: payload}
	raw, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != TypeData {
		t.Fatalf("type byte %d", raw[0])
	}
	if got := binary.BigEndian.Uint32(raw[1:5]); got != 0x01020304 {
		t.Fatalf("stream id %#x", got)
	}
	out, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != in.Type || out.StreamID != in.StreamID || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	// Payload is copied.
	raw[5] ^= 0xff
	if out.Payload[0] == raw[5] && len(payload) > 0 {
		t.Fatal("decode aliases the input buffer")
	}
}

func TestDecodeEmptyPayload(t *testing.T) {
	raw, err := Encode(Frame{Type: TypePing, StreamID: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != HeaderSize {
		t.Fatalf("len %d", len(raw))
	}
	f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != TypePing || f.StreamID != 0 || len(f.Payload) != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestPayloadLimit(t *testing.T) {
	ok := Frame{Type: TypeData, StreamID: 1, Payload: make([]byte, MaxPayload)}
	if _, err := Encode(ok); err != nil {
		t.Fatal(err)
	}
	too := Frame{Type: TypeData, StreamID: 1, Payload: make([]byte, MaxPayload+1)}
	if _, err := Encode(too); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("encode: %v", err)
	}
	raw := make([]byte, HeaderSize+MaxPayload+1)
	raw[0] = TypeData
	if _, err := Decode(raw); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("decode: %v", err)
	}
}

func TestShortFrame(t *testing.T) {
	if _, err := Decode(nil); !errors.Is(err, ErrShortFrame) {
		t.Fatal(err)
	}
	if _, err := Decode([]byte{TypeData, 0, 0, 0}); !errors.Is(err, ErrShortFrame) {
		t.Fatal(err)
	}
}

func TestHelloJSON(t *testing.T) {
	b, err := MarshalHello(Hello{Role: "agent", Key: "test-key", Name: "my-pc"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := UnmarshalHello(b)
	if err != nil {
		t.Fatal(err)
	}
	if h.Role != "agent" || h.Key != "test-key" || h.Name != "my-pc" {
		t.Fatalf("%+v", h)
	}
	if _, err := UnmarshalHello([]byte("{")); err == nil {
		t.Fatal("expected json error")
	}
	if _, err := UnmarshalHello(nil); err == nil {
		t.Fatal("expected size error")
	}
}

func TestValidName(t *testing.T) {
	ok := []string{"my-pc", "a.b", "PC_1", "n"}
	bad := []string{"", " ", "has space", "a\nb", "line\r", string(make([]byte, 129))}
	for _, s := range ok {
		if !ValidName(s) {
			t.Fatalf("want valid %q", s)
		}
	}
	for _, s := range bad {
		if ValidName(s) {
			t.Fatalf("want invalid %q", s)
		}
	}
}
