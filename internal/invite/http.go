package invite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/pairing"
)

// RedeemResponse is what kite-agent install persists. Token is shown once.
type RedeemResponse struct {
	Agent string `json:"agent"`
	Token string `json:"token"`
}

// Redeem posts code and name to the relay and returns the agent token.
// explicit is true when the operator set --name; otherwise a name bound to
// the code replaces name. The code is not consumed when the relay rejects it.
func Redeem(ctx context.Context, relayURL, code, name string, explicit bool) (token, agent string, err error) {
	base, err := pairing.HTTPBase(relayURL)
	if err != nil {
		return "", "", err
	}
	body, err := json.Marshal(map[string]any{"code": code, "name": name, "explicit_name": explicit})
	if err != nil {
		return "", "", err
	}
	endpoint := strings.TrimRight(base, "/") + "/invite/redeem"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kite-agent")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", "", errorsUnsupported
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var er struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &er)
		msg := strings.TrimSpace(er.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(payload))
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", "", fmt.Errorf("%s", msg)
	}
	var out RedeemResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", "", fmt.Errorf("install code response: %w", err)
	}
	if out.Token == "" || out.Agent == "" {
		return "", "", fmt.Errorf("relay redeem response was incomplete")
	}
	return out.Token, out.Agent, nil
}

// RevokeToken asks the relay to drop the agent token just issued, so a
// failed install does not leave an orphan credential. Best-effort.
func RevokeToken(ctx context.Context, relayURL, token, name string) error {
	base, err := pairing.HTTPBase(relayURL)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"token": token, "name": name})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(base, "/") + "/invite/revoke"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kite-agent")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(payload))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

var errorsUnsupported = fmt.Errorf("this relay does not support install codes; upgrade kite-relay")
