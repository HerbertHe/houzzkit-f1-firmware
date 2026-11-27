package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	reply := manager.mapping(hardwareRequest{Operation: "bind", Candidate: candidate.ID})
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
	if reply := restarted.mapping(hardwareRequest{Operation: "bind", Candidate: newCandidate.ID}); reply.Code != 409 {
		t.Fatalf("unplugged replacement unexpectedly bound: %+v", reply)
	}
	if binding, err := restarted.binding(); err != nil || binding == nil || *binding != known {
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
