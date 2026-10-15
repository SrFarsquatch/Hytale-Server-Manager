package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type AdminConfig struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
}

type Session struct {
	Username string
	Expires  time.Time
}

func (a *App) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	a.cfgMu.RLock()
	configured := a.cfg.Admin != nil
	a.cfgMu.RUnlock()
	jsonOut(w, 200, map[string]any{"configured": configured})
}

func (a *App) handleBootstrapCreate(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Password string }
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if len(strings.TrimSpace(body.Username)) < 2 || len(body.Password) < 8 {
		jsonError(w, 400, "username must be at least 2 characters and password at least 8 characters")
		return
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg.Admin != nil {
		jsonError(w, 409, "admin account already exists")
		return
	}
	h, err := hashPassword(body.Password)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	a.cfg.Admin = &AdminConfig{Username: strings.TrimSpace(body.Username), PasswordHash: h}
	if err := a.saveConfigLocked(); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	token := a.newSession(body.Username)
	a.setSessionCookie(w, token)
	jsonOut(w, 201, map[string]any{"ok": true})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := clientAddress(r)
	now := time.Now()
	if blocked, wait := a.loginBlocked(key, now); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(maxInt(1, int(wait.Seconds()))))
		jsonError(w, http.StatusTooManyRequests, "too many failed login attempts; try again later")
		return
	}
	var body struct{ Username, Password string }
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	a.cfgMu.RLock()
	admin := a.cfg.Admin
	a.cfgMu.RUnlock()
	if admin == nil || admin.Username != body.Username || !verifyPassword(admin.PasswordHash, body.Password) {
		a.recordLoginFailure(key, now)
		jsonError(w, 401, "invalid username or password")
		return
	}
	a.clearLoginFailures(key)
	token := a.newSession(body.Username)
	a.setSessionCookie(w, token)
	jsonOut(w, 200, map[string]any{"ok": true})
}

const loginAttemptLimit = 10

const loginWindow = 15 * time.Minute

const loginBlockDuration = 15 * time.Minute

type loginAttempt struct {
	Count        int
	WindowStart  time.Time
	BlockedUntil time.Time
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}

func (a *App) loginBlocked(key string, now time.Time) (bool, time.Duration) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	if a.loginAttempts == nil {
		a.loginAttempts = make(map[string]loginAttempt)
	}
	attempt, ok := a.loginAttempts[key]
	if !ok {
		return false, 0
	}
	if now.Before(attempt.BlockedUntil) {
		return true, attempt.BlockedUntil.Sub(now)
	}
	if !attempt.WindowStart.IsZero() && now.Sub(attempt.WindowStart) >= loginWindow {
		delete(a.loginAttempts, key)
		return false, 0
	}
	if attempt.Count >= loginAttemptLimit {
		attempt.BlockedUntil = now.Add(loginBlockDuration)
		a.loginAttempts[key] = attempt
		return true, loginBlockDuration
	}
	return false, 0
}

func (a *App) recordLoginFailure(key string, now time.Time) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	if a.loginAttempts == nil {
		a.loginAttempts = make(map[string]loginAttempt)
	}
	attempt := a.loginAttempts[key]
	if attempt.WindowStart.IsZero() || now.Sub(attempt.WindowStart) >= loginWindow {
		attempt = loginAttempt{WindowStart: now}
	}
	attempt.Count++
	if attempt.Count >= loginAttemptLimit {
		attempt.BlockedUntil = now.Add(loginBlockDuration)
	}
	a.loginAttempts[key] = attempt
	// Bound memory if many unique addresses hit the login endpoint.
	if len(a.loginAttempts) > 4096 {
		for address, entry := range a.loginAttempts {
			if now.Sub(entry.WindowStart) >= loginWindow || (!entry.BlockedUntil.IsZero() && now.After(entry.BlockedUntil)) {
				delete(a.loginAttempts, address)
			}
		}
	}
}

func (a *App) clearLoginFailures(key string) {
	a.loginMu.Lock()
	delete(a.loginAttempts, key)
	a.loginMu.Unlock()
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := sessionCookie(r); err == nil {
		a.sessionsMu.Lock()
		delete(a.sessions, c.Value)
		a.sessionsMu.Unlock()
	}
	clearSessionCookies(w)
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := sessionCookie(r)
		if err != nil || !a.validSession(c.Value) {
			jsonError(w, 401, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) newSession(username string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	a.sessionsMu.Lock()
	a.sessions[token] = Session{Username: username, Expires: time.Now().Add(7 * 24 * time.Hour)}
	a.sessionsMu.Unlock()
	return token
}

func (a *App) validSession(token string) bool {
	a.sessionsMu.Lock()
	defer a.sessionsMu.Unlock()
	s, ok := a.sessions[token]
	if !ok || time.Now().After(s.Expires) {
		delete(a.sessions, token)
		return false
	}
	return true
}

func (a *App) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", MaxAge: 7 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: false})
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	iter := 180000
	key := pbkdf2SHA256([]byte(password), salt, iter, 32)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iter, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	hLen := 32
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	for i := 1; i <= blocks; i++ {
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		var ib [4]byte
		binary.BigEndian.PutUint32(ib[:], uint32(i))
		_, _ = mac.Write(ib[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for j := 1; j < iterations; j++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for k := range t {
				t[k] ^= u[k]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// sessionCookieName is the login cookie. Logins from before the rename used
// one of legacySessionCookies; they stay valid until they expire.
const sessionCookieName = "realmkeeper_session"

var legacySessionCookies = []string{"hsm_session", "orbis_session"}

func sessionCookie(r *http.Request) (*http.Cookie, error) {
	for _, name := range append([]string{sessionCookieName}, legacySessionCookies...) {
		if c, err := r.Cookie(name); err == nil {
			return c, nil
		}
	}
	return nil, http.ErrNoCookie
}

func clearSessionCookies(w http.ResponseWriter) {
	for _, name := range append([]string{sessionCookieName}, legacySessionCookies...) {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
}
