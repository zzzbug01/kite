package relay

import (
	"errors"
	"net/http"
	"time"

	"github.com/zzzbug01/kite/internal/invite"
	"github.com/zzzbug01/kite/internal/pairing"
	"github.com/zzzbug01/kite/internal/protocol"
)

func (s *Server) handleInviteRedeem(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.inviteLim.AllowAll(time.Now(), pairing.Rule{Key: "invite:ip:" + ip, Limit: 30, Window: 10 * time.Minute}) {
		s.log.Info("invite redeem rejected", "remote", ip, "result", "rate limited")
		writeAPIError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	var req struct {
		Code         string `json:"code"`
		Name         string `json:"name"`
		ExplicitName bool   `json:"explicit_name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad request")
		return
	}
	if protocol.CheckAgentName(req.Name) != nil && req.Name != "" {
		writeAPIError(w, http.StatusBadRequest, "agent name is invalid")
		return
	}
	token, agent, err := s.invites.Redeem(req.Code, req.Name, req.ExplicitName, time.Now())
	if err != nil {
		s.log.Info("invite redeem rejected", "remote", ip, "result", err.Error())
		writeAPIError(w, inviteStatus(err), err.Error())
		return
	}
	s.log.Info("invite redeem", "agent", agent, "remote", ip)
	writeJSON(w, http.StatusOK, map[string]string{"agent": agent, "token": token})
}

func (s *Server) handleInviteRevoke(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.inviteLim.AllowAll(time.Now(), pairing.Rule{Key: "invite-revoke:ip:" + ip, Limit: 30, Window: 10 * time.Minute}) {
		writeAPIError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	var req struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad request")
		return
	}
	if !s.invites.Authenticate(req.Token, req.Name) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if _, err := s.invites.Revoke(req.Name); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.dropRevokedAgents()
	writeJSON(w, http.StatusOK, map[string]string{"agent": req.Name, "revoked": "true"})
}

func inviteStatus(err error) int {
	switch {
	case errors.Is(err, invite.ErrExpired):
		return http.StatusGone
	case errors.Is(err, invite.ErrUsed), errors.Is(err, invite.ErrName):
		return http.StatusConflict
	case errors.Is(err, invite.ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusBadRequest
	}
}
