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

func requestHost(r *http.Request, authority string) string {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok {
		return ""
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		host = authority
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
			if !strings.Contains(host, ":") {
				return ""
			}
		} else if strings.Contains(host, ":") {
			return ""
		}
	} else if port != strconv.Itoa(local.Port) {
		return ""
	}
	if isDeviceHostname(host) {
		return strings.ToLower(host)
	}
	if ip := net.ParseIP(host); ip != nil && local.IP.Equal(ip) {
		if strings.Contains(host, ":") {
			return "[" + strings.ToLower(host) + "]"
		}
		return ip.String()
	}
	return ""
}

func redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	host := requestHost(r, r.Host)
	if host == "" {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isDeviceHostname(host) {
		host += ":443"
	}
	http.Redirect(w, r, "https://"+host+"/", http.StatusTemporaryRedirect)
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
	host := requestHost(r, r.Host)
	if host == "" {
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
		origin, https := strings.CutPrefix(r.Header.Get("Origin"), "https://")
		if !https || requestHost(r, origin) != host {
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
