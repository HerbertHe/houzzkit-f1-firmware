package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkbenchRoutingAndSession(t *testing.T) {
	handler := newWebHandler()
	state := "ready"
	handler.request = func(*setupRequest) result { return result{State: state} }
	handler.authenticate = func(string) result { return result{State: "authenticated"} }

	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		ctx := context.WithValue(context.Background(), http.LocalAddrContextKey,
			&net.TCPAddr{IP: net.ParseIP("192.168.55.1"), Port: 443})
		r := httptest.NewRequestWithContext(ctx, method, "https://"+deviceHostname+path, strings.NewReader(body))
		r.Header.Set("Origin", "https://"+deviceHostname)
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
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
