package httpapi

import (
	"testing"

	"usesesame.app/backend/internal/clientaddr/clientaddrtest"
)

func TestClientAddressCases(t *testing.T) {
	for _, tc := range clientaddrtest.Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			service := &api{config: Config{TrustedProxies: tc.Prefixes()}}
			if got := service.clientIP(tc.Request()); got != tc.Client {
				t.Fatalf("clientIP = %q, want %q", got, tc.Client)
			}
			if got := service.clientLimitKey(tc.Request()); got != tc.LimitKey {
				t.Fatalf("clientLimitKey = %q, want %q", got, tc.LimitKey)
			}
		})
	}
}
