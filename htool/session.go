package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "__Host-htoold"
const sessionTTL = 30 * time.Minute

type sessionStore struct {
	mu      sync.Mutex
	expires map[string]time.Time
}

func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (s *sessionStore) valid(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expires[sessionToken(r)].After(time.Now())
}

func (s *sessionStore) create(w http.ResponseWriter, r *http.Request) bool {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return false
	}
	token := hex.EncodeToString(entropy[:])
	now := time.Now()
	expires := now.Add(sessionTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, until := range s.expires {
		if !until.After(now) {
			delete(s.expires, key)
		}
	}
	if old := sessionToken(r); old != "" {
		delete(s.expires, old)
	}
	if len(s.expires) >= 32 {
		oldest := token
		until := expires
		for key, expiry := range s.expires {
			if expiry.Before(until) {
				oldest, until = key, expiry
			}
		}
		delete(s.expires, oldest)
	}
	s.expires[token] = expires
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	return true
}

func (s *sessionStore) revoke(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.expires, sessionToken(r))
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

type loginLimiter struct {
	mu   sync.Mutex
	last time.Time
}

func (l *loginLimiter) attempt() bool {
	if !l.mu.TryLock() {
		return false
	}
	now := time.Now()
	if now.Sub(l.last) < 2*time.Second {
		l.mu.Unlock()
		return false
	}
	l.last = now
	return true
}

func validLoginPassword(password string) bool {
	return len(password) > 0 && len(password) <= 512 && !strings.ContainsRune(password, 0)
}

func (h webHandler) login(w http.ResponseWriter, r *http.Request) {
	status := h.request(nil)
	if status.Code != 0 {
		writeResult(w, unavailable())
		return
	}
	if status.State != "complete" {
		writeResult(w, result{Code: 409, Error: "请先完成设备初始化。"})
		return
	}
	var credentials struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &credentials) {
		return
	}
	if !validLoginPassword(credentials.Password) {
		writeResult(w, result{Code: 400, Error: "请输入有效的管理员密码。"})
		return
	}
	if !h.loginLimiter.attempt() {
		w.Header().Set("Retry-After", "2")
		writeResult(w, result{Code: 429, Error: "登录过于频繁，请稍后重试。"})
		return
	}
	defer h.loginLimiter.mu.Unlock()
	reply := h.authenticate(credentials.Password)
	if reply.Code == 429 {
		w.Header().Set("Retry-After", "2")
	}
	if reply.State == "authenticated" && reply.Code == 0 {
		if !h.sessions.create(w, r) {
			reply = unavailable()
		}
	}
	writeResult(w, reply)
}

func (h webHandler) logout(w http.ResponseWriter, r *http.Request) {
	h.sessions.revoke(w, r)
	writeResult(w, result{State: "logged-out"})
}

func writeDeviceInfo(w http.ResponseWriter) {
	name, _ := os.Hostname()
	var firmware struct {
		Version string `json:"version"`
	}
	if data, err := os.ReadFile("/etc/houzzkit-firmware.json"); err == nil {
		_ = json.Unmarshal(data, &firmware)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"hostname": name, "firmware": firmware.Version})
}
