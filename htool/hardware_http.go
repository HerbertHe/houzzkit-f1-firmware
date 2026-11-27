package main

import (
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"time"
)

var candidateID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func requestHardware(request hardwareRequest) hardwareReply {
	raw, err := net.DialTimeout("unix", hardwareSocket, 2*time.Second)
	if err != nil {
		return hardwareError(503, "硬件管理服务暂时不可用。")
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(9 * time.Second))
	if json.NewEncoder(conn).Encode(request) != nil || conn.CloseWrite() != nil {
		return hardwareError(503, "无法连接硬件管理服务。")
	}
	var envelope hardwareEnvelope
	if decodeLimitedJSON(conn, &envelope, 256<<10) != nil {
		return hardwareError(503, "硬件管理服务未返回有效结果。")
	}
	envelope.Reply.Code = envelope.Code
	return envelope.Reply
}

func (h webHandler) hardware(w http.ResponseWriter, r *http.Request, operation string) {
	if !h.sessions.valid(r) {
		writeResult(w, result{Code: 401, Error: "请以系统管理员身份登录。"})
		return
	}
	status := h.request(nil)
	if status.Code != 0 || status.State != "complete" {
		writeResult(w, unavailable())
		return
	}
	request := hardwareRequest{Operation: operation}
	if operation == "" {
		if !readJSON(w, r, &request) {
			return
		}
		bind := request.Operation == "bind" && candidateID.MatchString(request.Candidate)
		unbind := request.Operation == "unbind" && request.Candidate == ""
		if (!bind && !unbind) || !bindingRevisionPattern.MatchString(request.Revision) {
			writeResult(w, result{Code: 400, Error: "无效的设备绑定操作。"})
			return
		}
	}
	reply := requestHardware(request)
	w.Header().Set("Content-Type", "application/json")
	if reply.Code != 0 {
		w.WriteHeader(reply.Code)
	}
	_ = json.NewEncoder(w).Encode(reply)
}
