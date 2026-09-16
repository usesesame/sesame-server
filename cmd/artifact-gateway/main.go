// Package main is the private artifact gateway. It answers a request only
// when it carries the exact expiry and HMAC signature pair the API signs,
// then serves the file at the requested object key from its read-only data
// directory. Unconfigured, it stays up but refuses every artifact request.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const macPrefix = "sesame-artifact-gateway-v1\n"

func main() {
	dataDir := strings.TrimSpace(os.Getenv("SESAME_ARTIFACT_GATEWAY_DATA_DIR"))
	if dataDir == "" {
		dataDir = "/data"
	}
	artifact, ready, err := handler(strings.TrimSpace(os.Getenv("SESAME_ARTIFACT_GATEWAY_SIGNING_KEY")), dataDir)
	if err != nil {
		slog.Error("Sesame artifact gateway configuration is invalid", "error", err)
		os.Exit(1)
	}
	if !ready {
		slog.Warn("Sesame artifact gateway is disabled", "reason", "SESAME_ARTIFACT_GATEWAY_SIGNING_KEY is not configured")
	}
	addr := strings.TrimSpace(os.Getenv("SESAME_ARTIFACT_GATEWAY_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8791"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           livezHandler(ready, artifact),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("Sesame artifact gateway listening", "address", addr, "enabled", ready)
	if serveErr := server.ListenAndServe(); serveErr != nil {
		slog.Error("Sesame artifact gateway stopped", "error", serveErr)
		os.Exit(1)
	}
}

func livezHandler(ready bool, artifact http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/livez" {
			response.Header().Set("Content-Type", "application/json")
			if _, err := response.Write([]byte(`{"status":"ok"}`)); err != nil {
				slog.Warn("Sesame artifact gateway liveness write failed", "error", err)
			}
			return
		}
		artifact.ServeHTTP(response, request)
	})
}

func handler(key, dataDir string) (http.Handler, bool, error) {
	if strings.TrimSpace(key) == "" {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			response.WriteHeader(http.StatusServiceUnavailable)
		}), false, nil
	}
	signingKey, err := decodeKey(key)
	if err != nil {
		return nil, false, err
	}
	if info, statErr := os.Stat(dataDir); statErr != nil || !info.IsDir() {
		return nil, false, os.ErrNotExist
	}
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, false, err
	}
	artifact := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		objectKey := strings.TrimPrefix(request.URL.Path, "/")
		if !validObjectKey(objectKey) {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if !authorized(request, signingKey, objectKey) {
			response.WriteHeader(http.StatusForbidden)
			return
		}
		cleaned, cleanErr := filepath.Abs(filepath.Join(root, filepath.FromSlash(objectKey)))
		if cleanErr != nil || cleaned != root+"/"+objectKey {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if info, lstatErr := os.Lstat(cleaned); lstatErr != nil || !info.Mode().IsRegular() {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		file, openErr := os.Open(cleaned)
		if openErr != nil {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		defer func() { _ = file.Close() }()
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		http.ServeContent(response, request, info.Name(), info.ModTime(), file)
	})
	return artifact, true, nil
}

func authorized(request *http.Request, key []byte, objectKey string) bool {
	expires := request.URL.Query().Get("expires")
	signature := request.URL.Query().Get("signature")
	seconds, parseErr := strconv.ParseInt(expires, 10, 64)
	if parseErr != nil || expires != strconv.FormatInt(seconds, 10) || seconds < time.Now().Unix() {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(macPrefix + objectKey + "\n" + expires))
	expected := mac.Sum(nil)
	provided, decodeErr := base64.RawURLEncoding.DecodeString(signature)
	if decodeErr != nil || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, expected) == 1
}

func validObjectKey(value string) bool {
	if len(value) == 0 || len(value) > 1024 || strings.HasPrefix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._/-", character)) {
			return false
		}
	}
	return true
}

func decodeKey(raw string) ([]byte, error) {
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		return nil, errors.New("SESAME_ARTIFACT_GATEWAY_SIGNING_KEY must contain a base64url 32-byte key")
	}
	return key, nil
}
