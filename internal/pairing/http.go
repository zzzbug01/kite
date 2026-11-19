package pairing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/config"
)

// CreateRequest is POST /pair. Key is the agent registration key, or an
// agent token when Auth is "agent". It is checked and discarded; it is not
// written into the pairing code.
type CreateRequest struct {
	Key        string `json:"key"`
	Auth       string `json:"auth,omitempty"`
	Agent      string `json:"agent"`
	SSHUser    string `json:"ssh_user,omitempty"`
	ClientURL  string `json:"client_url"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// CreateResponse is the pairing code and its expiry. Code is the only secret.
type CreateResponse struct {
	Code      string    `json:"code"`
	Agent     string    `json:"agent"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RedeemRequest is POST /pair/redeem. There is no registration key.
type RedeemRequest struct {
	Code      string `json:"code"`
	PublicKey string `json:"public_key"`
	Label     string `json:"label"`
}

// RedeemResponse is what the connecting client persists.
// ClientToken is shown once and is not the registration key.
type RedeemResponse struct {
	Agent       string `json:"agent"`
	SSHUser     string `json:"ssh_user"`
	RelayURL    string `json:"relay_url"`
	ClientToken string `json:"client_token"`
}

// APIError is a JSON error body from the pairing endpoints.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("pairing request failed (%d)", e.Status)
	}
	return e.Message
}

// HTTPBase converts a relay address to the HTTP origin used for POST /pair
// and POST /invite/redeem. A bare host, host:port, and http(s) URLs are
// accepted. The path is dropped; Caddy and nginx proxy the whole host.
func HTTPBase(relayURL string) (string, error) {
	u, err := config.ParseRelayURL(relayURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("relay URL must be ws:// or wss://, got %q", u.Scheme)
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// ClientURL is the WebSocket URL a paired laptop should dial.
// An agent URL ending in /agent becomes /client. Any other path is replaced
// with /client as well, which matches kite's default routes. A bare host
// and an https URL are accepted.
func ClientURL(relayURL string) (string, error) {
	u, err := config.ParseRelayURL(relayURL)
	if err != nil {
		return "", err
	}
	u.Path = "/client"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// PostCreate asks the relay for a pairing code.
func PostCreate(ctx context.Context, httpBase string, req CreateRequest) (CreateResponse, error) {
	var out CreateResponse
	err := postJSON(ctx, strings.TrimRight(httpBase, "/")+"/pair", req, &out)
	return out, err
}

// PostRedeem exchanges a pairing code and a public key for a client token.
func PostRedeem(ctx context.Context, httpBase string, req RedeemRequest) (RedeemResponse, error) {
	var out RedeemResponse
	err := postJSON(ctx, strings.TrimRight(httpBase, "/")+"/pair/redeem", req, &out)
	return out, err
}

func postJSON(ctx context.Context, endpoint string, body any, dest any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return &APIError{Status: resp.StatusCode, Message: "this relay does not support pairing; upgrade kite-relay"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var er struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &er)
		if er.Error == "" {
			er.Error = strings.TrimSpace(string(payload))
		}
		if er.Error == "" {
			er.Error = resp.Status
		}
		return &APIError{Status: resp.StatusCode, Message: er.Error}
	}
	if dest == nil {
		return nil
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return fmt.Errorf("pairing response: %w", err)
	}
	return nil
}
