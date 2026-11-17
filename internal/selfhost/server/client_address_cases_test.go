package server

import (
	"testing"

	"usesesame.app/backend/internal/clientaddr/clientaddrtest"
)

func TestClientAddressCases(t *testing.T) {
	for _, tc := range clientaddrtest.Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			s := &server{cfg: Config{TrustedProxies: tc.Prefixes()}}
			if got := s.clientIP(tc.Request()); got != tc.Client {
				t.Fatalf("clientIP = %q, want %q", got, tc.Client)
			}
			if got := s.clientLimitKey(tc.Request()); got != tc.LimitKey {
				t.Fatalf("clientLimitKey = %q, want %q", got, tc.LimitKey)
			}
		})
	}
}
