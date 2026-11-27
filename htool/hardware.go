package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const hardwareSocket = "/run/htool-hardware.sock"
const hardwareStateDir = "/var/lib/htoold-hardware"
const hardwareRuntimeDir = "/run/htoold-hardware"

type hardwareRequest struct {
	Operation string `json:"operation"`
	Candidate string `json:"candidate,omitempty"`
	Revision  string `json:"revision,omitempty"`
}

type mappingStatus struct {
	Revision   string            `json:"revision"`
	State      string            `json:"state"`
	Alias      string            `json:"alias"`
	Device     string            `json:"device,omitempty"`
	Binding    *deviceIdentity   `json:"binding,omitempty"`
	Candidates []serialCandidate `json:"candidates"`
}

type hardwareReply struct {
	Mapping  *mappingStatus `json:"mapping,omitempty"`
	Reports  []bootReport   `json:"reports,omitempty"`
	Checking bool           `json:"checking,omitempty"`
	Code     int            `json:"-"`
	Error    string         `json:"error,omitempty"`
}

type hardwareManager struct {
	mu                                    sync.Mutex
	reportMu                              sync.Mutex
	recording                             bool
	reportError                           string
	configDir, stateDir, alias, ownedLink string
	inventory                             func() ([]serialCandidate, error)
}

func hardwareError(code int, message string) hardwareReply {
	return hardwareReply{Code: code, Error: message}
}

const initialBindingRevision = "00000000000000000000000000000000"

var bindingRevisionPattern = regexp.MustCompile(`^([a-f0-9]{32}|[a-f0-9]{64})$`)

type bindingRecord struct {
	Revision string          `json:"revision"`
	Device   *deviceIdentity `json:"device"`
}

func (m *hardwareManager) binding() (bindingRecord, error) {
	path := filepath.Join(m.configDir, "zigbee.json")
	var record bindingRecord
	err := readHardwareState(path, &record)
	if errors.Is(err, os.ErrNotExist) {
		return bindingRecord{Revision: initialBindingRevision}, nil
	}
	if err == nil {
		if !bindingRevisionPattern.MatchString(record.Revision) || (record.Device != nil && !validBinding(*record.Device)) {
			return bindingRecord{}, errors.New("invalid binding record")
		}
		return record, nil
	}
	// Older firmware stored a bare device identity.
	var legacy deviceIdentity
	if readHardwareState(path, &legacy) == nil && validBinding(legacy) {
		return bindingRecord{Revision: serialCandidateID("", "", "", legacy), Device: &legacy}, nil
	}
	return bindingRecord{}, err
}

func validBinding(identity deviceIdentity) bool {
	return identity.Kind == "usb" || identity.Kind == "uart"
}

func readHardwareState(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return decodeLimitedJSON(f, out, 48<<10)
}

func saveHardwareState(path string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return atomicWrite(path, jsonData)
}

func matchesIdentity(candidates []serialCandidate, binding *deviceIdentity) []serialCandidate {
	var matches []serialCandidate
	if binding != nil {
		for _, c := range candidates {
			if c.Identity == *binding {
				matches = append(matches, c)
			}
		}
	}
	return matches
}

func (m *hardwareManager) removeAlias() error {
	target, err := os.Readlink(m.alias)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && target != m.ownedLink {
		return errors.New("coordinator alias is owned by another process")
	}
	if err == nil {
		if err := os.Remove(m.alias); err != nil {
			return err
		}
	}
	if err := os.Remove(m.ownedLink); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *hardwareManager) enableAlias(target string) error {
	alias, err := os.Readlink(m.alias)
	if err == nil && alias != m.ownedLink {
		return errors.New("coordinator alias is owned by another process")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if current, err := os.Readlink(m.ownedLink); err != nil || current != target {
		staging, err := os.MkdirTemp(filepath.Dir(m.ownedLink), ".link-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(staging)
		temporary := filepath.Join(staging, "coordinator")
		if err := os.Symlink(target, temporary); err != nil {
			return err
		}
		if err := os.Rename(temporary, m.ownedLink); err != nil {
			return err
		}
	}
	if alias == "" {
		return os.Symlink(m.ownedLink, m.alias)
	}
	return nil
}

func (m *hardwareManager) reconcile(binding *deviceIdentity, candidates []serialCandidate) (mappingStatus, error) {
	status := mappingStatus{Alias: m.alias, Binding: binding, State: "unbound", Candidates: candidates}
	if len(status.Candidates) > 48 {
		status.Candidates = status.Candidates[:48]
	}
	matches := matchesIdentity(candidates, binding)
	var err error
	switch {
	case binding == nil:
		err = m.removeAlias()
	case len(matches) == 0:
		status.State = "missing"
		err = m.removeAlias()
	case len(matches) != 1:
		status.State = "conflict"
		err = m.removeAlias()
	case matches[0].awaitingPath:
		status.State = "waiting"
		err = m.removeAlias()
	case !matches[0].Available:
		status.State = "blocked"
		err = m.removeAlias()
	default:
		target := matches[0].target()
		if target == "" {
			status.State = "waiting"
			err = m.removeAlias()
		} else {
			status.State, status.Device = "connected", matches[0].Path
			err = m.enableAlias(target)
		}
	}
	if err != nil {
		status.State = "error"
	}
	return status, err
}

func (m *hardwareManager) mapping(request hardwareRequest) hardwareReply {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, err := m.binding()
	if err != nil {
		_ = m.removeAlias()
		return hardwareError(503, "无法读取已保存的设备绑定。")
	}
	candidates, err := m.inventory()
	if err != nil {
		_ = m.removeAlias()
		return hardwareError(503, "无法读取串口设备。")
	}
	if err := m.recoverPending(old.Device, candidates); err != nil {
		return hardwareError(503, "上次绑定操作未能恢复，请检查固定设备路径。")
	}
	if request.Operation == "mapping" {
		status, _ := m.reconcile(old.Device, candidates)
		status.Revision = old.Revision
		return hardwareReply{Mapping: &status}
	}
	if request.Revision != old.Revision {
		return hardwareError(409, "绑定已变更，请刷新后再操作。")
	}
	var replacement *deviceIdentity
	if request.Operation == "bind" {
		for _, candidate := range candidates {
			if candidate.ID == request.Candidate && candidate.Available {
				identity := candidate.Identity
				replacement = &identity
				break
			}
		}
		if replacement == nil || len(matchesIdentity(candidates, replacement)) != 1 {
			return hardwareError(409, "设备已断开、不可绑定或身份重复，请刷新列表。")
		}
	}
	var randomRevision [16]byte
	if _, err := rand.Read(randomRevision[:]); err != nil {
		return hardwareError(503, "无法生成新的绑定修订号。")
	}
	next := bindingRecord{Revision: hex.EncodeToString(randomRevision[:]), Device: replacement}
	path := filepath.Join(m.configDir, "zigbee.json")
	pending := m.pendingPath()
	if err := saveHardwareState(pending, struct {
		Target *deviceIdentity `json:"target"`
	}{replacement}); err != nil {
		return hardwareError(503, "无法保存待提交的设备绑定。")
	}

	current, err := m.inventory()
	if err != nil {
		m.abortPending(old.Device, nil)
		return hardwareError(503, "无法重新确认设备状态。")
	}
	if replacement != nil {
		matches := matchesIdentity(current, replacement)
		if len(matches) != 1 || matches[0].ID != request.Candidate {
			m.abortPending(old.Device, current)
			return hardwareError(409, "设备已变化，请刷新列表后重新选择。")
		}
	}
	status, err := m.reconcile(replacement, current)
	if err != nil || (replacement != nil && status.State != "connected") {
		m.abortPending(old.Device, current)
		return hardwareError(409, "无法应用新的设备绑定，请刷新列表。")
	}
	if err := saveHardwareState(path, next); err != nil {
		// Atomic rename can succeed before the directory sync fails.
		if restoreErr := saveHardwareState(path, old); restoreErr != nil {
			_ = m.removeAlias()
			fmt.Fprintf(os.Stderr, "restore coordinator binding: %v\n", restoreErr)
		} else {
			m.abortPending(old.Device, current)
		}
		return hardwareError(503, "无法提交新的设备绑定，请检查系统日志。")
	}
	// The committed file is authoritative if cleanup is interrupted here.
	if err := removeStateFile(pending); err != nil {
		fmt.Fprintf(os.Stderr, "clear committed coordinator transaction: %v\n", err)
	}
	status.Revision = next.Revision
	return hardwareReply{Mapping: &status}
}

func (m *hardwareManager) pendingPath() string {
	return filepath.Join(m.configDir, "zigbee.pending.json")
}

func (m *hardwareManager) recoverPending(binding *deviceIdentity, candidates []serialCandidate) error {
	if _, err := os.Lstat(m.pendingPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	// Never adopt the pending target. Reapply the last durable commit.
	if _, err := m.reconcile(binding, candidates); err != nil {
		return err
	}
	return removeStateFile(m.pendingPath())
}

func (m *hardwareManager) abortPending(binding *deviceIdentity, candidates []serialCandidate) {
	if err := m.recoverPending(binding, candidates); err != nil {
		_ = m.removeAlias()
		fmt.Fprintf(os.Stderr, "recover coordinator transaction: %v\n", err)
	}
}

func removeStateFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func runHardware(listener net.Listener) error {
	uid, err := webUID()
	if err != nil {
		return err
	}
	m := &hardwareManager{stateDir: hardwareStateDir, configDir: "/etc/htoold", recording: true, alias: coordinatorAlias,
		ownedLink: filepath.Join(hardwareRuntimeDir, "coordinator"), inventory: serialInventory}
	go m.observe()
	go m.recordBoot()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			if !isPeer(conn, uid) {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
			var request hardwareRequest
			if decodeJSON(conn, &request) != nil {
				return
			}
			var reply hardwareReply
			switch request.Operation {
			case "mapping", "bind", "unbind":
				reply = m.mapping(request)
			case "reports":
				reply = m.reports()
			default:
				reply = hardwareError(400, "操作不受支持。")
			}
			_ = json.NewEncoder(conn).Encode(hardwareEnvelope{Reply: reply, Code: reply.Code})
		}()
	}
}

func (m *hardwareManager) observe() {
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	events := hardwareEvents()
	last := ""
	for {
		reply := m.mapping(hardwareRequest{Operation: "mapping"})
		state := reply.Error
		if reply.Mapping != nil {
			state = reply.Mapping.State
		}
		if state != last {
			fmt.Fprintf(os.Stderr, "zigbee coordinator: %s\n", state)
			last = state
		}
		select {
		case <-events:
		case <-timer.C:
		}
	}
}

type hardwareEnvelope struct {
	Reply hardwareReply `json:"reply"`
	Code  int           `json:"code"`
}
