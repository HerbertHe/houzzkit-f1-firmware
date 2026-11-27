package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkbenchRoutingAndSession(t *testing.T) {
	handler := newWebHandler()
	state := "ready"
	handler.request = func(*setupRequest) result { return result{State: state} }
	handler.authenticate = func(string) result { return result{State: "authenticated"} }

	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		return workbenchRequest(handler, method, path, body, "https://"+deviceHostname, cookie)
	}

	if w := request(http.MethodGet, "/", "", nil); w.Code != 302 || w.Header().Get("Location") != "/welcome" {
		t.Fatal("uninitialized root did not redirect to welcome")
	}
	state = "complete"
	if w := request(http.MethodGet, "/", "", nil); w.Code != 302 || w.Header().Get("Location") != "/login" {
		t.Fatal("initialized root did not redirect to login")
	}
	if w := request(http.MethodGet, "/api/v1/workbench", "", nil); w.Code != 401 {
		t.Fatal("workbench API allowed anonymous access")
	}
	login := request(http.MethodPost, "/api/v1/login", `{"password":"test-password"}`, nil)
	cookies := login.Result().Cookies()
	if login.Code != 200 || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("login did not create a secure session")
	}
	cookie := cookies[0]
	if w := request(http.MethodGet, "/firmware", "", cookie); w.Code != 200 {
		t.Fatal("valid session did not allow a workbench deep link")
	}
	request(http.MethodPost, "/api/v1/logout", "", cookie)
	if w := request(http.MethodGet, "/api/v1/workbench", "", cookie); w.Code != 401 {
		t.Fatal("logged-out session could still be used")
	}
}

func workbenchRequest(handler http.Handler, method, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	ctx := context.WithValue(context.Background(), http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("192.168.55.1"), Port: 443})
	r := httptest.NewRequestWithContext(ctx, method, "https://"+deviceHostname+path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestWorkbenchLoginFailuresAndCrossOrigin(t *testing.T) {
	h := newWebHandler()
	h.request = func(*setupRequest) result { return result{State: "complete"} }
	attempts := 0
	h.authenticate = func(password string) result {
		attempts++
		if password == "wrong" {
			return result{Code: http.StatusUnauthorized}
		}
		return result{State: "authenticated"}
	}
	origin := "https://" + deviceHostname
	login := func(body, origin string) *httptest.ResponseRecorder {
		return workbenchRequest(h, http.MethodPost, "/api/v1/login", body, origin, nil)
	}
	if w := login(`{"password":"valid"}`, "https://other.local"); w.Code != 403 || attempts != 0 {
		t.Fatal("cross-origin login reached authentication")
	}
	if w := login(`{"password":"wrong"}`, origin); w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("invalid password created a session")
	}
	if w := login(`{"password":"valid"}`, origin); w.Code != 429 || w.Header().Get("Retry-After") == "" || attempts != 1 {
		t.Fatal("repeated login was not throttled before authentication")
	}
	h.loginLimiter.mu.Lock()
	blocked := login(`{"password":"valid"}`, origin)
	h.loginLimiter.mu.Unlock()
	if blocked.Code != 429 || attempts != 1 {
		t.Fatal("concurrent login was not throttled")
	}
	h.loginLimiter.last = time.Now().Add(-time.Minute)
	w := login(`{"password":"valid"}`, origin)
	cookies := w.Result().Cookies()
	if w.Code != 200 || len(cookies) != 1 {
		t.Fatal("valid login did not establish a session")
	}
	cookie := cookies[0]
	if w := workbenchRequest(h, http.MethodPost, "/api/v1/logout", "", "https://other.local", cookie); w.Code != 403 {
		t.Fatal("cross-origin logout was accepted")
	}
	if w := workbenchRequest(h, http.MethodGet, "/api/v1/workbench", "", origin, cookie); w.Code != 200 {
		t.Fatal("cross-origin logout revoked the valid session")
	}

	restarted := newWebHandler()
	restarted.request = h.request
	if w := workbenchRequest(restarted, http.MethodGet, "/api/v1/workbench", "", origin, cookie); w.Code != 401 {
		t.Fatal("session remained valid across daemon restart")
	}
	h.sessions.expires[cookie.Value] = time.Now().Add(-time.Minute)
	if w := workbenchRequest(h, http.MethodGet, "/api/v1/workbench", "", origin, cookie); w.Code != 401 {
		t.Fatal("expired session was accepted by the API")
	}
	if w := workbenchRequest(h, http.MethodGet, "/", "", origin, cookie); w.Code != 302 || w.Header().Get("Location") != "/login" {
		t.Fatal("expired session did not redirect the root page to login")
	}
}

func TestWorkbenchRecoveryForUnavailableState(t *testing.T) {
	h := newWebHandler()
	h.request = func(*setupRequest) result { return unavailable() }
	origin := "https://" + deviceHostname
	for _, path := range []string{"/", "/welcome", "/login", "/firmware"} {
		w := workbenchRequest(h, http.MethodGet, path, "", origin, nil)
		if w.Code != 503 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") ||
			!strings.Contains(w.Body.String(), "设备状态暂时不可用") || w.Header().Get("Location") != "" {
			t.Fatalf("path %s did not show the recovery page", path)
		}
	}
	if w := workbenchRequest(h, http.MethodHead, "/", "", origin, nil); w.Code != 503 || w.Body.Len() != 0 {
		t.Fatal("recovery HEAD response contained a body or wrong status")
	}
	if w := workbenchRequest(h, http.MethodGet, "/api/v1/workbench", "", origin, nil); w.Code != 503 ||
		w.Header().Get("Content-Type") != "application/json" {
		t.Fatal("workbench API did not keep its JSON error response")
	}
}
