package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const hardwareSocket = "/run/htool-hardware.sock"
const hardwareStateDir = "/var/lib/htoold-hardware"
const hardwareRuntimeDir = "/run/htoold-hardware"

type hardwareRequest struct {
	Operation string `json:"operation"`
	Candidate string `json:"candidate,omitempty"`
}

type mappingStatus struct {
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

func (m *hardwareManager) binding() (*deviceIdentity, error) {
	var identity deviceIdentity
	err := readHardwareState(filepath.Join(m.configDir, "zigbee.json"), &identity)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err == nil && identity.Kind != "usb" && identity.Kind != "uart" {
		err = errors.New("invalid device identity")
	}
	return &identity, err
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
	case len(matches) != 1 || !matches[0].Available:
		status.State = "conflict"
		err = m.removeAlias()
	default:
		target := matches[0].target()
		if target == "" {
			status.State = "missing"
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
	if request.Operation == "mapping" {
		status, _ := m.reconcile(old, candidates)
		return hardwareReply{Mapping: &status}
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
	path := filepath.Join(m.configDir, "zigbee.json")
	if err := writeBinding(path, replacement); err != nil {
		// Atomic rename can succeed before the directory sync fails.
		m.restoreBinding(path, old, candidates)
		return hardwareError(503, "无法保存设备绑定。")
	}
	// Re-read the inventory after persisting the selection so an unplugged
	// device or identity collision cannot silently become a new binding.
	current, scanErr := m.inventory()
	if scanErr != nil {
		m.restoreBinding(path, old, nil)
		return hardwareError(503, "无法重新确认设备状态。")
	}
	status, err := m.reconcile(replacement, current)
	if err != nil || (replacement != nil && status.State != "connected") {
		m.restoreBinding(path, old, current)
		return hardwareError(409, "无法应用新的设备绑定，已尝试恢复原绑定。")
	}
	return hardwareReply{Mapping: &status}
}

func writeBinding(path string, binding *deviceIdentity) error {
	if binding != nil {
		return saveHardwareState(path, binding)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (m *hardwareManager) restoreBinding(path string, binding *deviceIdentity, candidates []serialCandidate) {
	if err := writeBinding(path, binding); err != nil {
		_ = m.removeAlias()
		fmt.Fprintf(os.Stderr, "restore coordinator binding: %v\n", err)
		return
	}
	_, _ = m.reconcile(binding, candidates)
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
