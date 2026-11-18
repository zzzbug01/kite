package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/authkeys"
	"github.com/zzzbug01/kite/internal/pairing"
	"github.com/zzzbug01/kite/internal/protocol"
)

func (s *Server) handlePairCreate(w http.ResponseWriter, r *http.Request) {
	var req pairing.CreateRequest
	if err := readJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad request")
		return
	}
	ip := clientIP(r)
	if !s.registrationOK(req.Key) {
		s.log.Info("pair create rejected", "remote", ip, "result", "unauthorized")
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !protocol.ValidName(req.Agent) {
		writeAPIError(w, http.StatusBadRequest, "agent name is invalid")
		return
	}
	agent := s.lookup(req.Agent)
	if agent == nil {
		s.log.Info("pair create rejected", "agent", req.Agent, "remote", ip, "result", "offline")
		writeAPIError(w, http.StatusNotFound, "agent is not connected")
		return
	}
	if !agent.supports(protocol.FeaturePair) {
		s.log.Info("pair create rejected", "agent", req.Agent, "remote", ip, "result", "unsupported")
		writeAPIError(w, http.StatusConflict, "agent does not support pairing; upgrade kite-agent on that machine")
		return
	}
	clientURL := strings.TrimSpace(req.ClientURL)
	if !strings.HasPrefix(clientURL, "ws://") && !strings.HasPrefix(clientURL, "wss://") {
		writeAPIError(w, http.StatusBadRequest, "client_url must be a ws:// or wss:// URL")
		return
	}
	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
		if ttl < time.Minute || ttl > pairing.MaxTTL {
			writeAPIError(w, http.StatusBadRequest, "ttl must be between 1 and 30 minutes")
			return
		}
	}
	code, exp, err := s.broker.Create(ip, req.Agent, strings.TrimSpace(req.SSHUser), clientURL, ttl)
	if err != nil {
		s.log.Info("pair create rejected", "agent", req.Agent, "remote", ip, "result", err.Error())
		status := http.StatusTooManyRequests
		writeAPIError(w, status, err.Error())
		return
	}
	s.log.Info("pair create", "agent", req.Agent, "remote", ip, "expires", exp.UTC().Format(time.RFC3339))
	writeJSON(w, http.StatusOK, pairing.CreateResponse{Code: code, Agent: req.Agent, ExpiresAt: exp.UTC()})
}

func (s *Server) handlePairRedeem(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	var req pairing.RedeemRequest
	if err := readJSON(r, &req); err != nil {
		_ = s.broker.TouchRedeem(ip)
		writeAPIError(w, http.StatusBadRequest, "bad request")
		return
	}
	pub, err := authkeys.ValidateEd25519(req.PublicKey)
	if err != nil {
		if err := s.broker.TouchRedeem(ip); err != nil {
			s.log.Info("pair redeem rejected", "remote", ip, "result", "rate limited")
			writeAPIError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		s.log.Info("pair redeem rejected", "remote", ip, "result", "bad key")
		writeAPIError(w, http.StatusBadRequest, "public key must be one ssh-ed25519 line")
		return
	}
	issued, err := s.broker.Redeem(ip, req.Code)
	if err != nil {
		s.log.Info("pair redeem rejected", "remote", ip, "result", err.Error())
		writeAPIError(w, redeemStatus(err), publicRedeemError(err))
		return
	}
	agent := s.lookup(issued.Agent)
	if agent == nil || !agent.supports(protocol.FeaturePair) {
		s.log.Info("pair redeem rejected", "agent", issued.Agent, "remote", ip, "result", "agent unavailable")
		writeAPIError(w, http.StatusConflict, "agent is not connected; the pairing code was consumed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := agent.installKey(ctx, protocol.PairInstall{
		RequestID: newID(),
		PublicKey: pub,
		Label:     pairing.SanitizeLabel(req.Label),
	})
	if err != nil {
		s.log.Info("pair redeem rejected", "agent", issued.Agent, "remote", ip, "result", "no response")
		writeAPIError(w, http.StatusConflict, "agent did not accept the pairing request; upgrade kite-agent on that machine if it is older than pairing support. The pairing code was consumed")
		return
	}
	if !res.OK {
		msg := strings.TrimSpace(res.Error)
		if msg == "" {
			msg = "agent rejected the public key"
		}
		s.log.Info("pair redeem rejected", "agent", issued.Agent, "remote", ip, "result", "agent error")
		writeAPIError(w, http.StatusConflict, msg)
		return
	}
	sshUser := strings.TrimSpace(res.SSHUser)
	if sshUser == "" {
		sshUser = issued.SSHUser
	}
	if sshUser == "" {
		writeAPIError(w, http.StatusConflict, "agent did not report an SSH user")
		return
	}
	raw, rec, err := s.tokens.Issue(issued.Agent, pairing.SanitizeLabel(req.Label), sshUser, time.Now())
	if err != nil {
		s.log.Info("pair redeem rejected", "agent", issued.Agent, "remote", ip, "result", "token store")
		writeAPIError(w, http.StatusInternalServerError, "could not store the client token")
		return
	}
	s.log.Info("pair redeem", "agent", issued.Agent, "remote", ip, "token_id", rec.ID, "label", rec.Label)
	writeJSON(w, http.StatusOK, pairing.RedeemResponse{
		Agent:       issued.Agent,
		SSHUser:     sshUser,
		RelayURL:    issued.ClientURL,
		ClientToken: raw,
	})
}

func redeemStatus(err error) int {
	switch {
	case errors.Is(err, pairing.ErrRateLimited):
		return http.StatusTooManyRequests
	case errors.Is(err, pairing.ErrCodeUsed):
		return http.StatusConflict
	case errors.Is(err, pairing.ErrCodeExpired):
		return http.StatusGone
	default:
		return http.StatusBadRequest
	}
}

func publicRedeemError(err error) string {
	switch {
	case errors.Is(err, pairing.ErrRateLimited):
		return "rate limited"
	case errors.Is(err, pairing.ErrCodeUsed):
		return "pairing code already used"
	case errors.Is(err, pairing.ErrCodeExpired):
		return "pairing code expired"
	case errors.Is(err, pairing.ErrCodeInvalid):
		return "pairing code is not valid"
	default:
		return "pairing code was rejected"
	}
}

func readJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	return dec.Decode(dest)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first := strings.TrimSpace(strings.Split(fwd, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
	}
	if host == "" {
		return "unknown"
	}
	return host
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *agentConn) supports(feature string) bool {
	return a.features[feature]
}

func (a *agentConn) installKey(ctx context.Context, req protocol.PairInstall) (protocol.PairResult, error) {
	if req.RequestID == "" {
		return protocol.PairResult{}, errors.New("missing request id")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return protocol.PairResult{}, err
	}
	if len(payload) > 8*1024 {
		return protocol.PairResult{}, errors.New("pair payload too large")
	}
	ch := make(chan protocol.PairResult, 1)
	a.pairMu.Lock()
	if a.pairWait == nil {
		a.pairWait = make(map[string]chan protocol.PairResult)
	}
	a.pairWait[req.RequestID] = ch
	a.pairMu.Unlock()
	defer func() {
		a.pairMu.Lock()
		delete(a.pairWait, req.RequestID)
		a.pairMu.Unlock()
	}()
	if err := a.write(protocol.Frame{Type: protocol.TypePairInstall, Payload: payload}); err != nil {
		return protocol.PairResult{}, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return protocol.PairResult{}, ctx.Err()
	case <-a.done:
		return protocol.PairResult{}, errors.New("agent disconnected")
	}
}

func (a *agentConn) deliverPair(res protocol.PairResult) {
	a.pairMu.Lock()
	ch := a.pairWait[res.RequestID]
	a.pairMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- res:
	default:
	}
}
