package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixPeerCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	uid := uint32(os.Getuid())
	if !isPeer(conn, uid) || isPeer(conn, uid^1) {
		t.Fatal("invalid peer credential matching")
	}
}
