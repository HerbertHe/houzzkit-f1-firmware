package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeAccounts struct {
	users       map[string]string
	passwords   map[string]string
	failRoot    bool
	blockedName string
	created     int
}

func (f *fakeAccounts) comment(name string) (string, bool, error) {
	marker, exists := f.users[name]
	return marker, exists, nil
}
func (f *fakeAccounts) nameAvailable(name string) (bool, error) {
	return name != f.blockedName, nil
}
func (f *fakeAccounts) create(name, marker string) error {
	f.users[name] = marker
	f.created++
	return nil
}
func (f *fakeAccounts) password(name, value string) error {
	if f.failRoot && name == "root" {
		return errors.New("interrupted root change")
	}
	f.passwords[name] = value
	return nil
}

func TestSetupRecoveryAndCompletion(t *testing.T) {
	dir := t.TempDir()
	accounts := &fakeAccounts{users: map[string]string{}, passwords: map[string]string{}, failRoot: true}
	state := &setup{accounts: accounts, statePath: filepath.Join(dir, "state"), guardPath: filepath.Join(dir, "guard")}
	request := setupRequest{Username: "homeuser", Password: "daily-password-1", RootPassword: "root-password-2"}

	if reply := state.apply(request); reply.Code != 503 {
		t.Fatalf("expected interruption: %+v", reply)
	}
	if status := state.status(); status.State != "pending" || status.Username != request.Username {
		t.Fatalf("expected a pending setup: %+v", status)
	}
	conflict := request
	conflict.Username = "another"
	if reply := state.apply(conflict); reply.Code != 409 {
		t.Fatalf("unexpected account was allowed: %+v", reply)
	}
	state.mu.Lock()
	if reply := state.apply(request); reply.Code != 409 {
		t.Fatalf("concurrent setup was allowed: %+v", reply)
	}
	state.mu.Unlock()

	accounts.failRoot = false
	restarted := &setup{accounts: accounts, statePath: state.statePath, guardPath: state.guardPath}
	if reply := restarted.apply(request); reply.State != "complete" {
		t.Fatalf("could not resume: %+v", reply)
	}
	if accounts.created != 1 || accounts.passwords["root"] != request.RootPassword ||
		accounts.passwords[request.Username] != request.Password {
		t.Fatal("wrong account changes")
	}
	for _, path := range []string{state.statePath, state.guardPath} {
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), request.Password) || strings.Contains(string(data), request.RootPassword) {
			t.Fatalf("missing journal or plaintext credentials in %s", path)
		}
	}
	if err := os.Remove(state.statePath); err != nil {
		t.Fatal(err)
	}
	if reply := restarted.apply(request); reply.Code != 409 || reply.State != "complete" {
		t.Fatalf("removed journal reopened registration: %+v", reply)
	}
}

func TestRejectExistingAndInvalidAccounts(t *testing.T) {
	dir := t.TempDir()
	users := &fakeAccounts{users: map[string]string{"existing": ""}, passwords: map[string]string{}, blockedName: "groupname"}
	state := &setup{accounts: users, statePath: filepath.Join(dir, "state"), guardPath: filepath.Join(dir, "guard")}
	for name, want := range map[string]int{"existing": 409, "groupname": 409, "root": 400, "htool": 400, "中文": 400, "two users": 400} {
		if reply := state.apply(setupRequest{name, "daily-password-1", "root-password-2"}); reply.Code != want {
			t.Fatalf("%q: %+v", name, reply)
		}
	}
	if users.created != 0 || len(users.passwords) != 0 || state.status().State != "ready" {
		t.Fatal("invalid input changed accounts")
	}
	for _, bad := range []string{"short", "has a space!", "中文password-1", "password-with-newline\n"} {
		if validPassword(bad) {
			t.Errorf("accepted invalid password shape")
		}
	}
}

func TestSetupHTTPOriginAndRequestValidation(t *testing.T) {
	calls := 0
	handler := webHandler{request: func(r *setupRequest) result {
		calls++
		return result{State: "complete", Username: r.Username}
	}}
	valid := `{"username":"homeuser","password":"daily-password-1","rootPassword":"root-password-2"}`
	for _, tc := range []struct {
		host, local, origin, body string
		want                      int
	}{
		{deviceHostname + ":80", "10.0.0.25", "https://" + deviceHostname + ":80", valid, 200},
		{"10.0.0.25:80", "10.0.0.25", "https://10.0.0.25:80", valid, 200},
		{"[fd00::25]:80", "fd00::25", "https://[fd00::25]:80", valid, 200},
		{"10.0.0.25:80", "10.0.0.25", "https://example.org", valid, 403},
		{"10.0.0.25:80", "10.0.0.25", "https://" + deviceHostname + ":80", valid, 403},
		{"10.0.0.26:80", "10.0.0.25", "https://10.0.0.26:80", valid, 400},
		{"10.0.0.25:8443", "10.0.0.25", "https://10.0.0.25:8443", valid, 400},
		{"other.local:80", "10.0.0.25", "https://other.local:80", valid, 400},
		{"10.0.0.25:80", "10.0.0.25", "https://10.0.0.25:80", valid + valid, 400},
		{"10.0.0.25:80", "10.0.0.25", "https://10.0.0.25:80", strings.Repeat("x", 4097), 400},
	} {
		local := &net.TCPAddr{IP: net.ParseIP(tc.local), Port: 80}
		ctx := context.WithValue(context.Background(), http.LocalAddrContextKey, local)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://"+tc.host+"/api/v1/setup", strings.NewReader(tc.body))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		if result.Code != tc.want {
			t.Fatalf("HTTP %d, expected %d", result.Code, tc.want)
		}
	}
	if calls != 3 {
		t.Fatalf("backend called %d times", calls)
	}
}

func TestCertificatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.pem")
	first, err := certificate(path, net.ParseIP("10.0.0.25"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := certificate(path, net.ParseIP("10.0.0.25"))
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{deviceHostname, "10.0.0.25"} {
		if err := first.Leaf.VerifyHostname(host); err != nil {
			t.Fatalf("missing certificate SAN for %s: %v", host, err)
		}
	}
	if string(first.Certificate[0]) != string(next.Certificate[0]) {
		t.Fatal("certificate changed on restart")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("certificate private key file must be owner-only")
	}
}
