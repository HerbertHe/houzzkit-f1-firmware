package main

import (
	"net"
	"syscall"
)

func isPeer(conn net.Conn, expected uint32) bool {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return false
	}
	var matches bool
	err = raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		matches = err == nil && cred.Uid == expected
	})
	return err == nil && matches
}
