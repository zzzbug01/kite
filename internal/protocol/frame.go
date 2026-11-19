// Package protocol is the kite multiplexing frame format.
//
// A frame is a 5-byte header followed by a payload of at most 256 KiB:
//
//	byte 0    type
//	bytes 1-4 stream id, big-endian uint32 (0 is reserved for connection control)
//	bytes 5…  payload
//
// Stream 0 carries handshake and heartbeat frames. SSH byte streams use ids
// allocated by the relay, up to 64 concurrent streams on one agent connection.
//
// Frame types:
//
//	1  hello       first frame. JSON payload, see Hello.
//	2  hello_ok    registration accepted (agents).
//	3  hello_err   handshake rejected. Payload is a short UTF-8 reason.
//	4  open        relay -> agent: dial the agent's configured local SSH address.
//	5  open_ok     stream is ready. The header stream id is the new stream.
//	6  open_err    stream failed. Payload is a short UTF-8 reason.
//	7  data        stream bytes.
//	8  close      stream or session failed; drop it.
//	9  end        write half of the stream is closed (TCP FIN). Keep reading.
//	10 ping         application heartbeat. Payload empty, stream id 0.
//	11 pong         heartbeat reply. Payload empty, stream id 0.
//	12 pair_install relay -> agent, stream 0. JSON, see PairInstall.
//	    Older agents ignore this frame. The relay then reports that the agent
//	    does not support pairing.
//	13 pair_result  agent -> relay, stream 0. JSON, see PairResult.
//	    Older relays never send pair_install, so they never receive this.
//
// The relay never tells the agent where to dial. An open frame's payload is
// ignored by the agent; the only address it connects to is its own
// LOCAL_SSH_ADDR.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	// HeaderSize is the fixed prefix of every frame.
	HeaderSize = 5
	// MaxPayload is the largest allowed payload (256 KiB).
	MaxPayload = 256 * 1024
	// MaxMessage is the largest WebSocket message a peer may send.
	MaxMessage = HeaderSize + MaxPayload
	// MaxStreams is the cap on concurrent streams per agent connection.
	MaxStreams = 64
	// ChunkSize is the payload size peers should use when slicing a byte stream.
	ChunkSize = 32 * 1024
	// PingInterval is how often each side sends an application ping.
	PingInterval = 20 * time.Second
	// DefaultIdleTimeout drops a connection with no received frames for this long.
	DefaultIdleTimeout = 90 * time.Second
	// MaxHelloBytes rejects oversized handshake payloads.
	MaxHelloBytes = 4 * 1024
)

// Frame types. Ping and pong values are part of the wire contract.
const (
	TypeHello    byte = 1
	TypeHelloOK  byte = 2
	TypeHelloErr byte = 3
	TypeOpen     byte = 4
	TypeOpenOK   byte = 5
	TypeOpenErr  byte = 6
	TypeData     byte = 7
	TypeClose    byte = 8
	TypeEnd      byte = 9
	TypePing     byte = 10
	TypePong     byte = 11
	// TypePairInstall asks a connected agent to append one SSH public key.
	// Stream id is 0. Peers that do not know this type ignore the frame.
	TypePairInstall byte = 12
	// TypePairResult is the agent's reply to TypePairInstall.
	TypePairResult byte = 13
)

// FeaturePair is advertised in Hello.Features by agents that accept
// TypePairInstall. Old agents omit it; old relays ignore the field.
const FeaturePair = "pair"

// Short reasons written into error frames. They are safe to show a client.
const (
	ReasonUnauthorized   = "unauthorized"
	ReasonBadHello       = "bad hello"
	ReasonAgentNotFound  = "agent not found"
	ReasonTooManyStreams = "too many streams"
	ReasonAgentGone      = "agent unavailable"
	ReasonOpenTimeout    = "open timeout"
	ReasonOpenFailed     = "open failed"
	ReasonDialFailed     = "dial local ssh failed"
	// ReasonHandshakeTimeout is sent when the first hello never arrives.
	// The connection is closed after this frame. It is not an idle timeout.
	ReasonHandshakeTimeout = "handshake timeout"
)

// HandshakeTimeout is how long an upgraded WebSocket may sit without a
// hello. Authenticated connections use IDLE_TIMEOUT instead.
const HandshakeTimeout = 10 * time.Second

// ErrShortFrame is returned when a buffer is smaller than the header.
var ErrShortFrame = errors.New("frame shorter than 5 bytes")

// ErrPayloadTooLarge is returned when a payload exceeds MaxPayload.
var ErrPayloadTooLarge = errors.New("payload exceeds 256KiB")

// Frame is one decoded multiplexing message.
type Frame struct {
	Type     byte
	StreamID uint32
	Payload  []byte
}

// Encode serializes f. The returned buffer is owned by the caller.
func Encode(f Frame) ([]byte, error) {
	if len(f.Payload) > MaxPayload {
		return nil, ErrPayloadTooLarge
	}
	buf := make([]byte, HeaderSize+len(f.Payload))
	buf[0] = f.Type
	binary.BigEndian.PutUint32(buf[1:5], f.StreamID)
	copy(buf[HeaderSize:], f.Payload)
	return buf, nil
}

// Decode parses one frame. The payload is copied, so the caller may reuse b.
func Decode(b []byte) (Frame, error) {
	if len(b) < HeaderSize {
		return Frame{}, ErrShortFrame
	}
	if len(b)-HeaderSize > MaxPayload {
		return Frame{}, ErrPayloadTooLarge
	}
	payload := make([]byte, len(b)-HeaderSize)
	copy(payload, b[HeaderSize:])
	return Frame{
		Type:     b[0],
		StreamID: binary.BigEndian.Uint32(b[1:5]),
		Payload:  payload,
	}, nil
}

// Hello is the JSON payload of a TypeHello frame.
//
// Agents send Role "agent" and Name. Clients send Role "client" and Agent
// (the agent they want). Key is the shared registration secret, unless Auth
// is "client" (a per-client token from pairing) or "agent" (a per-agent
// token from an install code).
//
// Auth and Features are optional. Peers from v0.1.0-rc.2 ignore unknown
// JSON fields, so a new client still completes a legacy handshake when it
// sends the shared registration key and leaves Auth empty. An empty Auth
// on an agent is still the registration key.
type Hello struct {
	Role     string   `json:"role"`
	Key      string   `json:"key"`
	Name     string   `json:"name,omitempty"`
	Agent    string   `json:"agent,omitempty"`
	Auth     string   `json:"auth,omitempty"`
	Features []string `json:"features,omitempty"`
}

// PairInstall is the JSON payload of TypePairInstall.
// The pairing code itself is not included; it never leaves the relay.
type PairInstall struct {
	RequestID string `json:"request_id"`
	PublicKey string `json:"public_key"`
	Label     string `json:"label"`
}

// PairResult is the JSON payload of TypePairResult.
type PairResult struct {
	RequestID string `json:"request_id"`
	OK        bool   `json:"ok"`
	SSHUser   string `json:"ssh_user,omitempty"`
	Error     string `json:"error,omitempty"`
}

// HasFeature reports whether name was advertised on a hello.
func HasFeature(features []string, name string) bool {
	for _, f := range features {
		if f == name {
			return true
		}
	}
	return false
}

// MarshalHello encodes a hello payload.
func MarshalHello(h Hello) ([]byte, error) {
	b, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxHelloBytes {
		return nil, errors.New("hello payload too large")
	}
	return b, nil
}

// UnmarshalHello decodes a hello payload.
func UnmarshalHello(b []byte) (Hello, error) {
	if len(b) == 0 || len(b) > MaxHelloBytes {
		return Hello{}, errors.New("invalid hello size")
	}
	var h Hello
	if err := json.Unmarshal(b, &h); err != nil {
		return Hello{}, err
	}
	return h, nil
}

// ValidName reports whether s can be used as an agent name or SSH host alias.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// CheckAgentName rejects an empty name, whitespace, and the reserved name
// "connect", which is the kite subcommand.
func CheckAgentName(s string) error {
	if strings.EqualFold(strings.TrimSpace(s), "connect") {
		return errors.New(`agent name "connect" is reserved by the kite command`)
	}
	if !ValidName(s) {
		return errors.New("agent name is invalid")
	}
	return nil
}
