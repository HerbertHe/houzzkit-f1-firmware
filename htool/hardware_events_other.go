//go:build !linux

package main

import "os"

func sameDeviceNumber(os.FileInfo, string) bool { return false }

func hardwareEvents() <-chan struct{} { return nil }
