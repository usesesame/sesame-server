package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
)

const probeTimeout = 3 * time.Second

func main() {
	if err := probe(context.Background(), os.LookupEnv, newClient()); err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
		os.Exit(1)
	}
}

func newClient() *http.Client {
	return &http.Client{
		Timeout:       probeTimeout,
		Transport:     &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func probe(ctx context.Context, lookup config.Lookup, client *http.Client) error {
	address := config.HealthAddress(lookup)
	target, err := config.ReadyURL(address)
	if err != nil {
		return fmt.Errorf("%s or %s is %q: %w", config.EnvAddr, config.EnvHostedAddr, address, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", target, response.StatusCode)
	}
	return nil
}
