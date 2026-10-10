package main

import (
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}
