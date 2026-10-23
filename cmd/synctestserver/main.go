//go:build synctestserver

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/memstore"
	"usesesame.app/backend/internal/syncv3http"
)

type host struct{}

func (host) Authenticate(response http.ResponseWriter, request *http.Request, operation string) (syncsvc.Caller, bool) {
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Sesame ")
	if token == "" {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return syncsvc.Caller{}, false
	}
	return syncsvc.Caller{OwnerKey: "owner-1", TokenDeviceID: "desktop-" + token, Token: token}, true
}

func main() {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	service, err := syncsvc.New(memstore.New(), syncsvc.Config{ReceiptKey: private})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	syncv3http.New(service, host{}).Register(mux)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("URL=http://%s\nKEY=%s\n", listener.Addr(), base64.RawURLEncoding.EncodeToString(public))
	if err := http.Serve(listener, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
