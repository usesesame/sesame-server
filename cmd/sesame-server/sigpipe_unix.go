//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

func suppressSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}
