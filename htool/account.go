package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type setupRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	RootPassword string `json:"rootPassword"`
}

type result struct {
	State    string `json:"state"`
	Username string `json:"username,omitempty"`
	Code     int    `json:"code,omitempty"`
	Error    string `json:"error,omitempty"`
}

type journal struct {
	Username string `json:"username"`
	Marker   string `json:"marker"`
	Complete bool   `json:"complete"`
}

type accounts interface {
	comment(string) (string, bool, error)
	nameAvailable(string) (bool, error)
	create(string, string) error
	password(string, string) error
}

type setup struct {
	mu                   sync.Mutex
	accounts             accounts
	statePath, guardPath string
	freshPath            string
	loginLimiter         loginLimiter
}

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)
var reservedNames = map[string]bool{"root": true, "htool": true, "admin": true, "administrator": true, "sudo": true, "docker": true}

func validPassword(password string) bool {
	if len(password) < 12 || len(password) > 72 {
		return false
	}
	for i := 0; i < len(password); i++ {
		if password[i] < 33 || password[i] > 126 {
			return false
		}
	}
	return true
}

func (r setupRequest) valid() bool {
	return usernamePattern.MatchString(r.Username) && !reservedNames[r.Username] &&
		validPassword(r.Password) && validPassword(r.RootPassword) && r.Password != r.RootPassword
}

func readJournal(path string) (journal, error) {
	var entry journal
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return entry, nil
	}
	if err != nil {
		return entry, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return entry, errors.New("invalid setup journal file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return entry, err
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return journal{}, err
	}
	marker, err := hex.DecodeString(strings.TrimPrefix(entry.Marker, "htool-"))
	if !usernamePattern.MatchString(entry.Username) || reservedNames[entry.Username] ||
		!strings.HasPrefix(entry.Marker, "htool-") || err != nil || len(marker) != 16 {
		return journal{}, errors.New("invalid setup journal contents")
	}
	return entry, nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".htool-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(dir string) error {
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func saveJournal(path string, entry journal) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return atomicWrite(path, data)
}

func (s *setup) readState() (journal, error) {
	guard, err := readJournal(s.guardPath)
	if err != nil {
		return journal{}, err
	}
	if guard.Username != "" {
		if !guard.Complete {
			return journal{}, errors.New("invalid completion marker")
		}
		return guard, nil
	}
	entry, err := readJournal(s.statePath)
	if err != nil || entry.Username != "" {
		return entry, err
	}
	const marker = "uninitialized\n"
	info, err := os.Lstat(s.freshPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(marker)) {
		return journal{}, errors.New("missing or invalid provisioning state")
	}
	fresh, err := os.ReadFile(s.freshPath)
	if err != nil || string(fresh) != marker {
		return journal{}, errors.New("missing or invalid provisioning state")
	}
	return journal{}, nil
}

func unavailable() result {
	return result{Code: 503, Error: "无法读取设备状态或完成操作。请稍后重试，或通过 SSH 或本机终端检查。"}
}

func describeState(entry journal) result {
	if entry.Complete {
		return result{State: "complete", Username: entry.Username}
	}
	if entry.Username != "" {
		return result{State: "pending", Username: entry.Username}
	}
	return result{State: "ready"}
}

func (s *setup) status() result {
	if !s.mu.TryLock() {
		return result{State: "running"}
	}
	defer s.mu.Unlock()
	entry, err := s.readState()
	if err != nil {
		return unavailable()
	}
	return describeState(entry)
}

func (s *setup) apply(request setupRequest) result {
	if !request.valid() {
		return result{Code: 400, Error: "请检查用户名和两组密码的规则。"}
	}
	if !s.mu.TryLock() {
		return result{Code: 409, Error: "设备正在处理另一项设置。"}
	}
	defer s.mu.Unlock()

	entry, err := s.readState()
	if err != nil {
		return unavailable()
	}
	if entry.Complete {
		return result{State: "complete", Username: entry.Username, Code: 409, Error: "设备已完成设置。"}
	}
	if entry.Username != "" && entry.Username != request.Username {
		return result{State: "pending", Username: entry.Username, Code: 409, Error: "请使用原用户名恢复未完成的设置。"}
	}
	comment, exists, err := s.accounts.comment(request.Username)
	if err != nil {
		return unavailable()
	}
	if exists && (entry.Marker == "" || comment != entry.Marker) {
		return result{Code: 409, Error: "此用户名已存在。"}
	}
	if entry.Username == "" {
		available, err := s.accounts.nameAvailable(request.Username)
		if err != nil {
			return unavailable()
		}
		if !available {
			return result{Code: 409, Error: "此用户名与已有用户组或主目录冲突。"}
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return unavailable()
		}
		entry = journal{Username: request.Username, Marker: "htool-" + hex.EncodeToString(nonce[:])}
		if err := saveJournal(s.statePath, entry); err != nil {
			return unavailable()
		}
	}
	// A durable pending journal now owns setup. Removing the image marker prevents
	// reopening registration if the completed account records are later lost.
	if err := os.Remove(s.freshPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return unavailable()
	}
	if err := syncDirectory(filepath.Dir(s.freshPath)); err != nil {
		return unavailable()
	}
	if !exists && s.accounts.create(entry.Username, entry.Marker) != nil {
		return unavailable()
	}
	if s.accounts.password(entry.Username, request.Password) != nil ||
		s.accounts.password("root", request.RootPassword) != nil {
		return unavailable()
	}
	entry.Complete = true
	if saveJournal(s.guardPath, entry) != nil {
		return unavailable()
	}
	// The independent completion marker is authoritative if this second write fails.
	_ = saveJournal(s.statePath, entry)
	return describeState(entry)
}

func (s *setup) authenticate(password string) result {
	if s.status().State != "complete" {
		return unavailable()
	}
	if !s.loginLimiter.attempt() {
		return result{Code: 429, Error: "登录过于频繁，请稍后重试。"}
	}
	defer s.loginLimiter.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/local/libexec/htoold-pam")
	cmd.Stdin = strings.NewReader(password)
	err := cmd.Run()
	if err == nil {
		return result{State: "authenticated"}
	}
	var status *exec.ExitError
	if errors.As(err, &status) && status.ExitCode() == 1 {
		return result{Code: 401, Error: "管理员密码错误或账号当前不可登录。"}
	}
	return unavailable()
}

type systemAccounts struct{}

func (systemAccounts) comment(name string) (string, bool, error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 7 && fields[0] == name {
			return fields[4], true, nil
		}
	}
	return "", false, nil
}

func (systemAccounts) nameAvailable(name string) (bool, error) {
	if _, err := user.LookupGroup(name); err == nil {
		return false, nil
	} else {
		var unknown user.UnknownGroupError
		if !errors.As(err, &unknown) {
			return false, err
		}
	}
	if _, err := os.Lstat(filepath.Join("/home", name)); !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

func (systemAccounts) create(name, marker string) error {
	cmd := exec.Command("/usr/sbin/useradd", "--create-home", "--user-group", "--shell", "/bin/bash",
		"--comment", marker, "--", name)
	return cmd.Run()
}

func (systemAccounts) password(name, value string) error {
	cmd := exec.Command("/usr/sbin/chpasswd")
	cmd.Stdin = strings.NewReader(name + ":" + value + "\n")
	return cmd.Run()
}

func decodeJSON(reader io.Reader, target any) error {
	return decodeLimitedJSON(reader, target, 4096)
}

func decodeLimitedJSON(reader io.Reader, target any, limit int64) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit {
		return errors.New("invalid request size")
	}
	return decodeStrictJSON(data, target)
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing input")
	}
	return nil
}
