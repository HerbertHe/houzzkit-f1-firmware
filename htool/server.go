package main

import (
	"embed"
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var assets embed.FS

const deviceHostname = "houzzkit.local"

func isDeviceHostname(host string) bool {
	return strings.EqualFold(strings.TrimSuffix(host, "."), deviceHostname)
}

func validHost(r *http.Request) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok {
		return false
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != strconv.Itoa(local.Port) {
		return false
	}
	return isDeviceHostname(host) || local.IP.Equal(net.ParseIP(host))
}

type accountRequest struct {
	Setup *setupRequest `json:"setup,omitempty"`
}

func requestAccount(request *setupRequest) result {
	raw, err := net.DialTimeout("unix", accountSocket, 2*time.Second)
	if err != nil {
		return unavailable()
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(33 * time.Second))
	if json.NewEncoder(conn).Encode(accountRequest{Setup: request}) != nil || conn.CloseWrite() != nil {
		return unavailable()
	}
	var reply result
	if decodeJSON(conn, &reply) != nil {
		return unavailable()
	}
	return reply
}

type webHandler struct {
	request func(*setupRequest) result
}

func writeResult(w http.ResponseWriter, reply result) {
	w.Header().Set("Content-Type", "application/json")
	if reply.Code != 0 {
		w.WriteHeader(reply.Code)
	}
	_ = json.NewEncoder(w).Encode(reply)
}

func (h webHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !validHost(r) {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/api/v1/setup":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Origin") != "https://"+r.Host {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			http.Error(w, "expected JSON", http.StatusUnsupportedMediaType)
			return
		}
		var request setupRequest
		if decodeJSON(r.Body, &request) != nil || !request.valid() {
			writeResult(w, result{Code: 400, Error: "请检查用户名和两组密码的规则。"})
			return
		}
		writeResult(w, h.request(&request))
	case "/api/v1/setup/status":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeResult(w, h.request(nil))
	case "/", "/style.css", "/app.js":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		http.ServeFileFS(w, r, assets, "web"+path)
	default:
		http.NotFound(w, r)
	}
}
