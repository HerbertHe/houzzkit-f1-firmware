package main

import (
	"fmt"
	"os"
	"syscall"
)

func sameDeviceNumber(info os.FileInfo, expected string) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	id := uint64(stat.Rdev)
	major := (id&0x00000000000fff00)>>8 | (id&0xfffff00000000000)>>32
	minor := (id & 0xff) | (id&0x00000ffffff00000)>>12
	return expected == fmt.Sprintf("%d:%d", major, minor)
}

func hardwareEvents() <-chan struct{} {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return nil
	}
	mask := uint32(syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MOVED_TO | syscall.IN_MOVED_FROM)
	_, devErr := syscall.InotifyAddWatch(fd, "/dev", mask)
	_, udevErr := syscall.InotifyAddWatch(fd, "/run/udev/data", mask|syscall.IN_CLOSE_WRITE)
	if devErr != nil && udevErr != nil {
		_ = syscall.Close(fd)
		return nil
	}
	events := make(chan struct{}, 1)
	go func() {
		defer syscall.Close(fd)
		buffer := make([]byte, 8192)
		for {
			if _, err := syscall.Read(fd, buffer); err != nil {
				if err == syscall.EINTR {
					continue
				}
				return
			}
			select {
			case events <- struct{}{}:
			default:
			}
		}
	}()
	return events
}
