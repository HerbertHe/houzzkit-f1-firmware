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
	"time"
)

func TestMappingPersistenceAndConflict(t *testing.T) {
	dir := t.TempDir()
	known := deviceIdentity{Kind: "uart", Board: "fixture", Topology: "/serial@fe680000"}
	candidate := serialCandidate{ID: "selected", Path: "/dev/ttyS4", Identity: known, Available: true}
	candidates := []serialCandidate{candidate}
	manager := &hardwareManager{
		stateDir: dir, configDir: dir, alias: filepath.Join(dir, "alias"),
		ownedLink: filepath.Join(dir, "coordinator"), inventory: func() ([]serialCandidate, error) {
			return candidates, nil
		},
	}

	reply := manager.mapping(hardwareRequest{Operation: "bind", Candidate: candidate.ID, Revision: initialBindingRevision})
	if reply.Mapping == nil || reply.Mapping.State != "connected" {
		t.Fatalf("binding failed: %+v", reply)
	}
	restarted := &hardwareManager{stateDir: dir, configDir: dir, alias: manager.alias,
		ownedLink: manager.ownedLink, inventory: manager.inventory}
	candidates = nil
	if reply := restarted.mapping(hardwareRequest{Operation: "mapping"}); reply.Mapping == nil ||
		reply.Mapping.State != "missing" || reply.Mapping.Binding == nil {
		t.Fatalf("unplug removed the persisted binding: %+v", reply)
	}
	if _, err := os.Lstat(manager.alias); !os.IsNotExist(err) {
		t.Fatalf("unplug retained the device alias: %v", err)
	}
	candidate.Path = "/dev/ttyS9"
	duplicate := candidate
	duplicate.Path = "/dev/ttyS10"
	candidates = []serialCandidate{candidate, duplicate}
	if reply := restarted.mapping(hardwareRequest{Operation: "mapping"}); reply.Mapping == nil ||
		reply.Mapping.State != "conflict" {
		t.Fatalf("duplicate identity was accepted: %+v", reply)
	}
	candidates = candidates[:1]
	if reply := restarted.mapping(hardwareRequest{Operation: "mapping"}); reply.Mapping == nil ||
		reply.Mapping.Device != candidate.Path || reply.Mapping.State != "connected" {
		t.Fatalf("device did not recover after re-enumeration: %+v", reply)
	}
	if link, err := os.Readlink(manager.ownedLink); err != nil || link != candidate.Path {
		t.Fatalf("incorrect coordinator path: %q, %v", link, err)
	}

	newCandidate := duplicate
	newCandidate.ID = "replacement"
	newCandidate.Identity.Topology = "/serial@fe6d0000"
	restarted.inventory = func() ([]serialCandidate, error) {
		list := candidates
		candidates = []serialCandidate{candidate}
		return list, nil
	}
	candidates = append(candidates, newCandidate)
	if reply := restarted.mapping(hardwareRequest{Operation: "bind", Candidate: newCandidate.ID, Revision: reply.Mapping.Revision}); reply.Code != 409 {
		t.Fatalf("unplugged replacement unexpectedly bound: %+v", reply)
	}
	if binding, err := restarted.binding(); err != nil || binding.Device == nil || *binding.Device != known {
		t.Fatalf("failed change did not restore previous binding: %+v, %v", binding, err)
	}
}

func TestRetainLastThreeBoots(t *testing.T) {
	var history []bootReport
	for _, id := range []string{"a", "b", "c", "d", "c"} {
		history = retainBootReport(history, bootReport{BootID: id})
	}
	if len(history) != 3 || history[0].BootID != "d" || history[1].BootID != "c" || history[2].BootID != "b" {
		t.Fatalf("unexpected retained order: %+v", history)
	}
}

func TestRecoverBootReports(t *testing.T) {
	m := &hardwareManager{stateDir: t.TempDir()}
	active := filepath.Join(m.stateDir, bootReportsFile)
	backup := filepath.Join(m.stateDir, invalidBootReportsFile)
	corrupt := []byte(`[{"bootId":`)
	if err := os.WriteFile(active, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.loadReports(); !errors.Is(err, errInvalidBootReports) {
		t.Fatalf("expected invalid history, got %v", err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("read-only lookup moved the invalid history: %v", err)
	}
	if history, err := m.recoverReports(); err != nil || len(history) != 0 {
		t.Fatalf("invalid history did not recover: %+v, %v", history, err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != string(corrupt) {
		t.Fatalf("invalid history was not preserved: %q, %v", data, err)
	}
	report := bootReport{
		BootID:  "12345678-1234-1234-1234-123456789abc",
		Checked: time.Now().UTC(),
		Checks:  []bootCheck{{Name: "eMMC", State: "ready"}},
	}
	if err := saveHardwareState(active, retainBootReport(nil, report)); err != nil {
		t.Fatal(err)
	}
	if history, err := m.recoverReports(); err != nil || len(history) != 1 || history[0].BootID != report.BootID {
		t.Fatalf("new report was not retained: %+v, %v", history, err)
	}
}

func TestBootReportReadFailureIsNotQuarantined(t *testing.T) {
	m := &hardwareManager{stateDir: t.TempDir()}
	active := filepath.Join(m.stateDir, bootReportsFile)
	if err := os.Mkdir(active, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.recoverReports(); err == nil || errors.Is(err, errInvalidBootReports) {
		t.Fatalf("expected filesystem error, got %v", err)
	}
	if info, err := os.Stat(active); err != nil || !info.IsDir() {
		t.Fatalf("unreadable path was moved: %v", err)
	}
}

func TestHardwareEndpointsRejectAnonymousRequests(t *testing.T) {
	h := newWebHandler()
	h.request = func(*setupRequest) result {
		t.Fatal("anonymous hardware request reached the account or hardware backend")
		return result{}
	}
	ctx := context.WithValue(context.Background(), http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("192.168.55.1"), Port: 443})
	for _, tc := range []struct {
		method, path, origin string
		code                 int
	}{
		{"GET", "/api/v1/hardware", "", 401},
		{"GET", "/api/v1/hardware/boot-reports", "", 401},
		{"POST", "/api/v1/hardware/binding", "https://houzzkit.local", 401},
		{"POST", "/api/v1/hardware/binding", "https://other.local", 403},
	} {
		r := httptest.NewRequestWithContext(ctx, tc.method, "https://houzzkit.local"+tc.path, strings.NewReader(""))
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s %s returned %d, want %d", tc.method, tc.path, w.Code, tc.code)
		}
	}
}

func TestPhysicalUSBLinkIgnoresDuplicateSerialAlias(t *testing.T) {
	serial := "/dev/serial/by-id/usb-identical"
	selected := "/dev/serial/by-path/first-port"
	duplicate := "/dev/serial/by-path/second-port"
	resolve := func(link string) (string, error) {
		if link == selected {
			return "/dev/ttyUSB0", nil
		}
		return "/dev/ttyUSB1", nil
	}
	if got := physicalUSBLink("/dev/ttyUSB0", []string{serial, duplicate, selected}, resolve); got != selected {
		t.Fatalf("selected %q instead of the verified port", got)
	}
	if got := physicalUSBLink("/dev/ttyUSB1", []string{serial}, resolve); got != "" {
		t.Fatalf("accepted the serial alias without a verified port: %q", got)
	}
}

func TestBootChecksRetrySlowHardware(t *testing.T) {
	if !checksNeedRetry([]bootCheck{{Name: "Wi-Fi", State: "missing"}}) ||
		!checksNeedRetry([]bootCheck{{Name: "蓝牙", State: "enumerated"}}) ||
		!checksNeedRetry([]bootCheck{{Name: "Zigbee", State: "missing"}}) {
		t.Fatal("late enumeration did not request another sample")
	}
	if checksNeedRetry([]bootCheck{
		{Name: "NVMe", State: "optional-absent"},
		{Name: "Wi-Fi", State: "enumerated"},
		{Name: "Zigbee", State: "unbound"},
	}) {
		t.Fatal("optional hardware or an unbound coordinator delayed reporting")
	}
}

func TestSerialCandidateRejectsStaleAttachment(t *testing.T) {
	device := deviceIdentity{Kind: "usb", Vendor: "1234", Product: "abcd", Interface: "00", Serial: "original"}
	path, tty := "/sys/devices/usb1/1-2/ttyUSB0", "/dev/ttyUSB0"
	original := serialCandidateID(path, tty, "12345", device)
	if again := serialCandidateID(path, tty, "12345", device); again != original {
		t.Fatal("same attachment did not keep its candidate ID")
	}
	if reattached := serialCandidateID(path, tty, "12346", device); reattached == original {
		t.Fatal("reattachment reused the old candidate ID")
	}
	device.Serial = "replacement"
	if swapped := serialCandidateID(path, tty, "12345", device); swapped == original {
		t.Fatal("different device reused the same port and candidate ID")
	}
}

func TestRecoverPendingBindingUsesLastDurableCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		dir := t.TempDir()
		old := serialCandidate{Path: "/dev/ttyS4", Identity: deviceIdentity{Kind: "uart", Topology: "first"}, Available: true}
		next := serialCandidate{Path: "/dev/ttyS9", Identity: deviceIdentity{Kind: "uart", Topology: "second"}, Available: true}
		want := old
		if committed {
			want = next
		}
		manager := &hardwareManager{
			configDir: dir, alias: filepath.Join(dir, "alias"), ownedLink: filepath.Join(dir, "coordinator"),
			inventory: func() ([]serialCandidate, error) { return []serialCandidate{old, next}, nil },
		}
		if err := saveHardwareState(filepath.Join(dir, "zigbee.json"), &want.Identity); err != nil {
			t.Fatal(err)
		}
		if err := saveHardwareState(manager.pendingPath(), map[string]deviceIdentity{"target": next.Identity}); err != nil {
			t.Fatal(err)
		}
		if err := manager.enableAlias(next.Path); err != nil {
			t.Fatal(err)
		}
		reply := manager.mapping(hardwareRequest{Operation: "mapping"})
		if reply.Mapping == nil || reply.Mapping.Device != want.Path {
			t.Fatalf("committed=%t: restored wrong alias: %+v", committed, reply)
		}
		if _, err := os.Lstat(manager.pendingPath()); !os.IsNotExist(err) {
			t.Fatalf("committed=%t: pending marker not cleaned up: %v", committed, err)
		}
		if link, err := os.Readlink(manager.ownedLink); err != nil || link != want.Path {
			t.Fatalf("committed=%t: restored path %q, error %v", committed, link, err)
		}
	}
}

func TestBootRetryDistinguishesWaitingFromConflict(t *testing.T) {
	dir := t.TempDir()
	device := deviceIdentity{Kind: "usb", Serial: "coordinator"}
	manager := &hardwareManager{alias: filepath.Join(dir, "alias"), ownedLink: filepath.Join(dir, "coordinator")}
	waiting := serialCandidate{Identity: device, awaitingPath: true}
	status, err := manager.reconcile(&device, []serialCandidate{waiting})
	if err != nil || status.State != "waiting" || !checksNeedRetry([]bootCheck{{Name: "Zigbee", State: status.State}}) {
		t.Fatalf("single unready USB device was not scheduled for retry: %+v, %v", status, err)
	}
	status, err = manager.reconcile(&device, []serialCandidate{waiting, waiting})
	if err != nil || status.State != "conflict" || checksNeedRetry([]bootCheck{{Name: "Zigbee", State: status.State}}) {
		t.Fatalf("duplicate identity was not distinguished from waiting: %+v, %v", status, err)
	}
}

func TestUSBStoredBindingUsesPortOnlyWithoutSerial(t *testing.T) {
	manager := &hardwareManager{configDir: t.TempDir()}
	for _, tc := range []struct {
		saved, expected deviceIdentity
	}{
		{deviceIdentity{Kind: "usb", Serial: "coordinator"}, deviceIdentity{Kind: "usb", Serial: "coordinator"}},
		{deviceIdentity{Kind: "usb", Serial: "coordinator", Topology: "old-port"}, deviceIdentity{Kind: "usb", Serial: "coordinator"}},
		{deviceIdentity{Kind: "usb", Topology: "same-port"}, deviceIdentity{Kind: "usb", Topology: "same-port"}},
	} {
		if err := saveHardwareState(filepath.Join(manager.configDir, "zigbee.json"),
			bindingRecord{Revision: initialBindingRevision, Device: &tc.saved}); err != nil {
			t.Fatal(err)
		}
		got, err := manager.binding()
		if err != nil || got.Revision != initialBindingRevision || got.Device == nil || *got.Device != tc.expected {
			t.Fatalf("reading %+v gave %+v, %v", tc.saved, got, err)
		}
	}
}

func TestStaleBindingRevisionRemainsInvalidAfterUnbind(t *testing.T) {
	dir := t.TempDir()
	c := serialCandidate{ID: "device", Path: "/dev/ttyS4", Available: true, Identity: deviceIdentity{Kind: "uart", Topology: "first"}}
	m := &hardwareManager{configDir: dir, alias: filepath.Join(dir, "alias"), ownedLink: filepath.Join(dir, "coordinator"),
		inventory: func() ([]serialCandidate, error) { return []serialCandidate{c}, nil }}
	first := m.mapping(hardwareRequest{Operation: "bind", Revision: initialBindingRevision, Candidate: c.ID})
	if first.Mapping == nil || first.Mapping.Revision == initialBindingRevision {
		t.Fatalf("bind did not advance revision: %+v", first)
	}
	if stale := m.mapping(hardwareRequest{Operation: "unbind", Revision: initialBindingRevision}); stale.Code != 409 {
		t.Fatalf("stale unbind accepted: %+v", stale)
	}
	state, err := m.binding()
	if err != nil || state.Device == nil || state.Revision != first.Mapping.Revision {
		t.Fatalf("stale request changed active state: %+v, %v", state, err)
	}
	unbound := m.mapping(hardwareRequest{Operation: "unbind", Revision: first.Mapping.Revision})
	if unbound.Mapping == nil || unbound.Mapping.Binding != nil || unbound.Mapping.Revision == initialBindingRevision {
		t.Fatalf("unbind did not persist a new revision: %+v", unbound)
	}
	restarted := &hardwareManager{configDir: dir, alias: m.alias, ownedLink: m.ownedLink, inventory: m.inventory}
	if stale := restarted.mapping(hardwareRequest{Operation: "bind", Revision: initialBindingRevision, Candidate: c.ID}); stale.Code != 409 {
		t.Fatalf("stale bind accepted after daemon restart: %+v", stale)
	}
}
