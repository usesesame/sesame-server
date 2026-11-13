package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	input, closeInput := interruptibleStdin(ctx, os.Stdin)
	code := run(ctx, os.Args[1:], defaultEnvironment(os.LookupEnv, input, os.Stdout, os.Stderr))
	closeInput()
	stop()
	os.Exit(code)
}
