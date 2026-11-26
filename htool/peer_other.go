//go:build !linux

package main

import "net"

func isPeer(net.Conn, uint32) bool { return false }
