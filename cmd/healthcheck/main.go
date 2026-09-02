package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	client := http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{}}
	response, err := client.Get("http://127.0.0.1:8787/readyz")
	if err != nil {
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
