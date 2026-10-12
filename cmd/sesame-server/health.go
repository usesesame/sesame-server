package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
)

const probeTimeout = 3 * time.Second

func healthcheck(ctx context.Context, invocation Invocation) error {
	client := &http.Client{
		Timeout:       probeTimeout,
		Transport:     &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return probeReady(ctx, invocation.Config.Addr, client)
}

func probeReady(ctx context.Context, addr string, client *http.Client) error {
	target, err := config.ReadyURL(addr)
	if err != nil {
		return fmt.Errorf("%s is %q: %w", config.EnvAddr, addr, err)
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
