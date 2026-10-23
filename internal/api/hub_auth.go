// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/hubsecurity"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/support"
	"github.com/SMH01/open-pixel-gun-3d-backend/internal/webauth"
	"github.com/go-webauthn/webauthn/protocol"
	wa "github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp"
)

const hubFlowCookie = "__Host-opg3d_flow"

type hubFlow struct {
	Kind         string
	Identity     webauth.Identity
	OwnerSession string
	Expires      time.Time
	Attempts     int
	Secret       string
	Ceremony     wa.SessionData
}

func sessionDigest(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func (s *Server) initHub() {
	s.hubFlows = map[string]*hubFlow{}
	if s.webAuth == nil {
		return
	}
	if len(s.cfg.HubSecurityKey) != 32 {
		for _, name := range []string{"hub-security.enc", "web-sessions.enc", "support.enc"} {
			if _, err := os.Stat(filepath.Join(s.cfg.DataDir, name)); !os.IsNotExist(err) {
				s.webAuth = nil
				s.log.Event("hub.security_key_missing", nil)
				break
			}
		}
		return
	}
	sessions, err := webauth.Open(filepath.Join(s.cfg.DataDir, "web-sessions.enc"), s.cfg.HubSecurityKey)
	if err != nil {
		s.webAuth = nil
		s.log.Event("hub.sessions_unavailable", nil)
		return
	}
	s.webAuth = sessions
	path := filepath.Join(s.cfg.DataDir, "hub-security.enc")
	s.hubSecurity, err = hubsecurity.Open(path, s.cfg.HubSecurityKey)
	if err != nil {
		s.log.Event("hub.security_unavailable", nil)
		s.webAuth = nil
		return
	} // fail CLOSED, never silently bypass existing MFA
	s.support, err = support.Open(filepath.Join(s.cfg.DataDir, "support.enc"), s.cfg.HubSecurityKey)
	if err != nil {
		s.log.Event("support.unavailable", nil)
	}
	u, err := url.Parse(s.cfg.WebAuthOrigin)
	if err != nil {
		return
	}
	s.hubWA, err = wa.New(&wa.Config{RPDisplayName: "OPG3D HUB", RPID: u.Hostname(), RPOrigins: []string{s.cfg.WebAuthOrigin}, AttestationPreference: protocol.PreferNoAttestation, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}, Timeouts: wa.TimeoutsConfig{Login: wa.TimeoutConfig{Enforce: true, Timeout: 3 * time.Minute}, Registration: wa.TimeoutConfig{Enforce: true, Timeout: 3 * time.Minute}}})
	if err != nil {
		s.hubWA = nil
		s.log.Event("hub.passkeys_unavailable", nil)
	}
}
func (s *Server) newHubFlow(w http.ResponseWriter, flow hubFlow) error {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return e
	}
	token := hex.EncodeToString(b[:])
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	now := time.Now()
	for k, v := range s.hubFlows {
		if !now.Before(v.Expires) {
			delete(s.hubFlows, k)
		}
	}
	if len(s.hubFlows) >= 2048 {
		return errors.New("flow limit")
	}
	flow.Expires = now.Add(3 * time.Minute)
	s.hubFlows[sessionDigest(token)] = &flow
	setWebCookie(w, hubFlowCookie, token, 180)
	return nil
}
func (s *Server) takeHubFlow(r *http.Request, kind string, consume bool) (hubFlow, bool) {
	k := sessionDigest(cookieValue(r, hubFlowCookie))
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	f := s.hubFlows[k]
	if f == nil || f.Kind != kind || !time.Now().Before(f.Expires) || f.Attempts >= 5 {
		return hubFlow{}, false
	}
	f.Attempts++
	copy := *f
	if consume || f.Attempts >= 5 {
		delete(s.hubFlows, k)
	}
	return copy, true
}
func (s *Server) dropHubFlow(r *http.Request) {
	s.hubMu.Lock()
	delete(s.hubFlows, sessionDigest(cookieValue(r, hubFlowCookie)))
	s.hubMu.Unlock()
}
func (s *Server) currentHubSession(r *http.Request, mutation, fresh bool) (webauth.Session, bool) {
	if !s.webRequestAllowed(r) {
		return webauth.Session{}, false
	}
	login, err := s.webAuth.Current(cookieValue(r, sessionCookie), func(i webauth.Identity) bool { return s.validHubSessionIdentity(i, webClientIP(r)) })
	if err != nil {
		return webauth.Session{}, false
	}
	if mutation && r.Header.Get("X-CSRF-Token") != login.CSRF {
		return webauth.Session{}, false
	}
	if fresh && time.Since(login.AuthenticatedAt) > 5*time.Minute {
		return webauth.Session{}, false
	}
	return login, true
}
func (s *Server) sendHubLogin(w http.ResponseWriter, r *http.Request, login webauth.Session) {
	s.webAuth.Revoke(cookieValue(r, sessionCookie))
	setWebCookie(w, sessionCookie, login.Token, int(webauth.SessionTTL.Seconds()))
	setWebCookie(w, challengeCookie, "", -1)
	setWebCookie(w, hubFlowCookie, "", -1)
	s.writeJSON(w, 200, map[string]any{"player_id": login.Identity.PlayerID, "csrf_token": login.CSRF, "expires_at": login.ExpiresAt, "scopes": []string{"hub", "exchange", "bug_reports"}})
}

// Returns true only when a second factor is required; the game OTP token is
// immediately revoked and NEVER returned as a full browser credential.
func (s *Server) stageHubMFA(w http.ResponseWriter, r *http.Request, login webauth.Session) bool {
	if s.hubSecurity == nil {
		return false
	}
	record := s.hubSecurity.Get(login.Identity.PlayerID, login.Identity.Fingerprint)
	if record.Secret == "" {
		return false
	}
	s.webAuth.Revoke(login.Token)
	s.webAuth.Revoke(cookieValue(r, sessionCookie))
	setWebCookie(w, sessionCookie, "", -1)
	setWebCookie(w, challengeCookie, "", -1)
	if e := s.newHubFlow(w, hubFlow{Kind: "mfa", Identity: login.Identity}); e != nil {
		s.writeError(w, 503, "security_unavailable", "")
		return true
	}
	s.writeJSON(w, 202, map[string]bool{"mfa_required": true})
	return true
}
func (s *Server) handleHubAuth(w http.ResponseWriter, r *http.Request) {
	s.hubAuthMu.Lock()
	defer s.hubAuthMu.Unlock()
	if !s.webRequestAllowed(r) || s.hubSecurity == nil {
		s.writeError(w, 503, "security_unavailable", "")
		return
	}
	if !s.webAuth.Limit("hub-ip:"+webClientIP(r), 60) {
		s.writeError(w, 429, "try_later", "")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/hub/auth/")
	if name == "mfa" {
		var body struct {
			Code string `json:"code"`
		}
		if !readWebJSON(w, r, &body) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		f, ok := s.takeHubFlow(r, "mfa", false)
		if !ok || !s.validWebIdentity(f.Identity, webClientIP(r)) || !s.webAuth.Limit("mfa-account:"+f.Identity.PlayerID, 20) || s.hubSecurity.Check(f.Identity.PlayerID, f.Identity.Fingerprint, strings.TrimSpace(body.Code)) != nil {
			s.writeError(w, 401, "invalid_second_factor", "")
			return
		}
		s.dropHubFlow(r)
		login, e := s.webAuth.Issue(s.sessionIdentity(f.Identity))
		if e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		s.sendHubLogin(w, r, login)
		return
	}
	if name == "passkey/login/begin" {
		if s.hubWA == nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		var body struct{}
		if !readWebJSON(w, r, &body) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		options, session, e := s.hubWA.BeginDiscoverableLogin(wa.WithUserVerification(protocol.VerificationRequired))
		if e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		if e = s.newHubFlow(w, hubFlow{Kind: "passkey-login", Ceremony: *session}); e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		s.writeJSON(w, 200, options)
		return
	}
	if name == "passkey/login/finish" {
		f, ok := s.takeHubFlow(r, "passkey-login", true)
		if !ok || s.hubWA == nil {
			s.writeError(w, 401, "invalid_passkey", "")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		user, credential, e := s.hubWA.FinishPasskeyLogin(func(rawID, handle []byte) (wa.User, error) {
			u, e := s.hubSecurity.FindUser(rawID, handle)
			if e != nil || !s.validWebIdentity(webauth.Identity{PlayerID: u.ID, Fingerprint: u.Record.Fingerprint}, webClientIP(r)) {
				return nil, hubsecurity.ErrInvalid
			}
			return u, nil
		}, f.Ceremony, r)
		if e != nil {
			s.writeError(w, 401, "invalid_passkey", "")
			return
		}
		u, ok := user.(hubsecurity.User)
		if !ok || s.hubSecurity.UpdateKey(u, *credential) != nil {
			s.writeError(w, 401, "invalid_passkey", "")
			return
		}
		login, e := s.webAuth.Issue(s.sessionIdentity(webauth.Identity{PlayerID: u.ID, Fingerprint: u.Record.Fingerprint}))
		if e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		s.sendHubLogin(w, r, login)
		return
	}
	login, ok := s.currentHubSession(r, r.Method != "GET", r.Method != "GET")
	if !ok {
		s.writeError(w, 403, "fresh_login_required", "")
		return
	}
	id, fp := login.Identity.PlayerID, login.Identity.Fingerprint
	if name == "settings" && r.Method == "GET" {
		rec := s.hubSecurity.Get(id, fp)
		keys := []map[string]any{}
		for _, k := range rec.Keys {
			keys = append(keys, map[string]any{"id": hubsecurity.KeyID(k.Credential), "name": k.Name, "created_at": k.CreatedAt})
		}
		s.writeJSON(w, 200, map[string]any{"totp_enabled": rec.Secret != "", "recovery_remaining": len(rec.Recovery), "passkeys": keys, "passkeys_available": s.hubWA != nil, "fresh": time.Since(login.AuthenticatedAt) <= 5*time.Minute})
		return
	}
	switch name {
	case "totp/begin":
		var body struct{}
		if !readWebJSON(w, r, &body) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		if s.hubSecurity.Get(id, fp).Secret != "" {
			s.writeError(w, 409, "already_enabled", "")
			return
		}
		if _, e := s.hubSecurity.Ensure(id, fp); e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		secret, uri, e := hubsecurity.NewTOTP(id)
		if e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		if e = s.newHubFlow(w, hubFlow{Kind: "totp-enroll", Identity: login.Identity, OwnerSession: sessionDigest(cookieValue(r, sessionCookie)), Secret: secret}); e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		key, _ := otp.NewKeyFromURL(uri)
		image, e := key.Image(240, 240)
		if e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		var pngBytes bytes.Buffer
		png.Encode(&pngBytes, image)
		s.writeJSON(w, 200, map[string]any{"secret": secret, "uri": uri, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes.Bytes())})
		return
	case "totp/confirm":
		var body struct {
			Code string `json:"code"`
		}
		if !readWebJSON(w, r, &body) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		f, ok := s.takeHubFlow(r, "totp-enroll", false)
		if !ok || f.Identity != login.Identity || f.OwnerSession != sessionDigest(cookieValue(r, sessionCookie)) {
			s.writeError(w, 401, "invalid_second_factor", "")
			return
		}
		recovery, e := s.hubSecurity.Enable(id, fp, f.Secret, strings.TrimSpace(body.Code))
		if e != nil {
			s.writeError(w, 401, "invalid_second_factor", "")
			return
		}
		s.dropHubFlow(r)
		s.rotateHubSession(w, r, login, recovery)
		return
	case "totp/disable", "recovery/renew":
		var body struct {
			Code string `json:"code"`
		}
		if !readWebJSON(w, r, &body) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		if s.hubSecurity.Check(id, fp, strings.TrimSpace(body.Code)) != nil {
			s.writeError(w, 401, "invalid_second_factor", "")
			return
		}
		var recovery []string
		var err error
		if name == "totp/disable" {
			err = s.hubSecurity.Disable(id, fp)
		} else {
			recovery, err = s.hubSecurity.Recovery(id, fp)
		}
		if err != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		s.rotateHubSession(w, r, login, recovery)
		return
	case "passkey/register/begin":
		var body struct{}
		if s.hubWA == nil || !readWebJSON(w, r, &body) {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		user, e := s.hubSecurity.Ensure(id, fp)
		if e != nil || len(user.Record.Keys) >= 8 {
			s.writeError(w, 409, "passkey_limit", "")
			return
		}
		options, session, e := s.hubWA.BeginRegistration(user)
		if e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		if e = s.newHubFlow(w, hubFlow{Kind: "passkey-register", Identity: login.Identity, OwnerSession: sessionDigest(cookieValue(r, sessionCookie)), Ceremony: *session}); e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		s.writeJSON(w, 200, options)
		return
	case "passkey/register/finish":
		f, ok := s.takeHubFlow(r, "passkey-register", true)
		if !ok || f.Identity != login.Identity || f.OwnerSession != sessionDigest(cookieValue(r, sessionCookie)) || s.hubWA == nil {
			s.writeError(w, 401, "invalid_passkey", "")
			return
		}
		user, e := s.hubSecurity.Ensure(id, fp)
		if e != nil {
			s.writeError(w, 503, "security_unavailable", "")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		credential, e := s.hubWA.FinishRegistration(user, f.Ceremony, r)
		if e != nil || s.hubSecurity.AddKey(id, fp, "Passkey", *credential) != nil {
			s.writeError(w, 401, "invalid_passkey", "")
			return
		}
		s.rotateHubSession(w, r, login, nil)
		return
	case "passkey/remove":
		var body struct {
			ID string `json:"id"`
		}
		if !readWebJSON(w, r, &body) || len(body.ID) > 2048 {
			s.writeError(w, 400, "invalid_request", "")
			return
		}
		if s.hubSecurity.RemoveKey(id, fp, body.ID) != nil {
			s.writeError(w, 404, "not_found", "")
			return
		}
		s.rotateHubSession(w, r, login, nil)
		return
	default:
		s.writeError(w, 404, "not_found", "")
	}
}
func (s *Server) rotateHubSession(w http.ResponseWriter, r *http.Request, previous webauth.Session, recovery []string) {
	s.webAuth.RevokeIdentity(previous.Identity)
	login, e := s.webAuth.IssueAt(s.sessionIdentity(previous.Identity), previous.AuthenticatedAt)
	if e != nil {
		s.writeError(w, 503, "security_unavailable", "")
		return
	}
	setWebCookie(w, sessionCookie, login.Token, int(webauth.SessionTTL.Seconds()))
	setWebCookie(w, hubFlowCookie, "", -1)
	s.writeJSON(w, 200, map[string]any{"csrf_token": login.CSRF, "recovery_codes": recovery, "ok": true})
}

func (s *Server) sessionIdentity(i webauth.Identity) webauth.Identity {
	if s.hubSecurity != nil {
		i.Epoch = s.hubSecurity.Get(i.PlayerID, i.Fingerprint).Epoch
	}
	return i
}
func (s *Server) validHubSessionIdentity(i webauth.Identity, ip string) bool {
	return s.validWebIdentity(i, ip) && i.Epoch == s.sessionIdentity(i).Epoch
}
