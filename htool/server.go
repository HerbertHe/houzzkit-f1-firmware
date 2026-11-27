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
	path := "/"
	switch r.URL.Path {
	case "/welcome", "/login", "/hardware/boot-checks", "/firmware":
		path = r.URL.Path
	}
	http.Redirect(w, r, "https://"+host+path, http.StatusTemporaryRedirect)
}

type accountRequest struct {
	Setup *setupRequest `json:"setup,omitempty"`
	Login *string       `json:"login,omitempty"`
}

func requestAccount(request accountRequest) result {
	raw, err := net.DialTimeout("unix", accountSocket, 2*time.Second)
	if err != nil {
		return unavailable()
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(33 * time.Second))
	if json.NewEncoder(conn).Encode(request) != nil || conn.CloseWrite() != nil {
		return unavailable()
	}
	var reply result
	if decodeJSON(conn, &reply) != nil {
		return unavailable()
	}
	return reply
}

type webHandler struct {
	request      func(*setupRequest) result
	authenticate func(string) result
	sessions     *sessionStore
	loginLimiter *loginLimiter
}

func newWebHandler() *webHandler {
	return &webHandler{
		request:      func(request *setupRequest) result { return requestAccount(accountRequest{Setup: request}) },
		authenticate: func(password string) result { return requestAccount(accountRequest{Login: &password}) },
		sessions:     &sessionStore{expires: make(map[string]time.Time)},
		loginLimiter: &loginLimiter{},
	}
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
	case "/api/v1/setup", "/api/v1/login", "/api/v1/logout", "/api/v1/hardware/binding":
		if !allowMethod(w, r, http.MethodPost) {
			return
		}
		origin, https := strings.CutPrefix(r.Header.Get("Origin"), "https://")
		if !https || requestHost(r, origin) != host {
			writeResult(w, result{Code: 403, Error: "无法确认请求来源。"})
			return
		}
		switch r.URL.Path {
		case "/api/v1/setup":
			var request setupRequest
			if !readJSON(w, r, &request) {
				return
			}
			if !request.valid() {
				writeResult(w, result{Code: 400, Error: "请检查用户名和两组密码的规则。"})
				return
			}
			reply := h.request(&request)
			if reply.State == "complete" && reply.Code != 0 {
				reply.Username = ""
			}
			writeResult(w, reply)
		case "/api/v1/login":
			h.login(w, r)
		case "/api/v1/logout":
			h.logout(w, r)
		case "/api/v1/hardware/binding":
			h.hardware(w, r, "")
		}
	case "/api/v1/setup/status":
		if allowMethod(w, r, http.MethodGet) {
			status := h.request(nil)
			if status.State == "complete" {
				status.Username = ""
			}
			writeResult(w, status)
		}
	case "/api/v1/hardware", "/api/v1/hardware/boot-reports":
		if allowMethod(w, r, http.MethodGet) {
			operation := "mapping"
			if r.URL.Path == "/api/v1/hardware/boot-reports" {
				operation = "reports"
			}
			h.hardware(w, r, operation)
		}
	case "/style.css", "/app.js", "/login.js", "/workspace.js", "/hardware.js":
		if allowMethod(w, r, http.MethodGet, http.MethodHead) {
			http.ServeFileFS(w, r, assets, "web"+r.URL.Path)
		}
	case "/", "/welcome", "/login", "/hardware/boot-checks", "/firmware", "/api/v1/workbench":
		if allowMethod(w, r, http.MethodGet, http.MethodHead) {
			h.page(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func allowMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeResult(w, result{Code: 405, Error: "请求方式不受支持。"})
	return false
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeResult(w, result{Code: 415, Error: "请求需要使用 JSON。"})
		return false
	}
	if decodeJSON(r.Body, target) != nil {
		writeResult(w, result{Code: 400, Error: "请求内容无效。"})
		return false
	}
	return true
}

func (h webHandler) page(w http.ResponseWriter, r *http.Request) {
	status := h.request(nil)
	initialized := status.State == "complete" && status.Code == 0
	ready := status.Code == 0 && (status.State == "ready" || status.State == "pending" || status.State == "running")
	path := r.URL.Path
	if !initialized && !ready {
		if path == "/api/v1/workbench" {
			writeResult(w, unavailable())
		} else {
			serveRecovery(w, r)
		}
		return
	}
	loggedIn := initialized && h.sessions.valid(r)
	if path == "/api/v1/workbench" {
		if !loggedIn {
			writeResult(w, result{Code: 401, Error: "请以系统管理员身份登录。"})
			return
		}
		writeDeviceInfo(w)
		return
	}
	switch {
	case !initialized && path != "/welcome":
		http.Redirect(w, r, "/welcome", http.StatusFound)
	case !initialized:
		http.ServeFileFS(w, r, assets, "web/welcome.html")
	case !loggedIn && path != "/login":
		http.Redirect(w, r, "/login", http.StatusFound)
	case !loggedIn:
		http.ServeFileFS(w, r, assets, "web/login.html")
	case path == "/welcome" || path == "/login":
		http.Redirect(w, r, "/", http.StatusFound)
	default:
		http.ServeFileFS(w, r, assets, "web/workspace.html")
	}
}

func serveRecovery(w http.ResponseWriter, r *http.Request) {
	page, err := assets.ReadFile("web/recovery.html")
	if err != nil {
		http.Error(w, unavailable().Error, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method != http.MethodHead {
		_, _ = w.Write(page)
	}
}
