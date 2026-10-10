package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/console"
	"usesesame.app/backend/internal/selfhost/updates"
)

const (
	FlagDesktopLinking = "desktop_linking_enabled"
	minimumPepperBytes = 16
)

func NewHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}

func FlagDefinitions() []selfhost.FlagDefinition {
	return []selfhost.FlagDefinition{
		{Key: FlagDesktopLinking, Description: "Let desktops link to this server with a pairing code.", Default: true},
	}
}

type Config struct {
	Store                selfhost.Store
	Version              string
	Commit               string
	PublicURL            config.PublicURL
	TrustedProxies       []netip.Prefix
	SigningKey           ed25519.PrivateKey
	CapabilityKeyID      string
	IPPepper             []byte
	Console              fs.FS
	Metrics              bool
	BackupInterval       time.Duration
	Warnings             []string
	MinimumClientVersion string
	MaximumClientVersion string
	LatestDesktopVersion string
	CapabilityTTL        time.Duration
	Updates              *updates.Service
	Now                  func() time.Time
}

func (c Config) Validate() error {
	if c.Store == nil {
		return errors.New("the store is required")
	}
	if len(c.SigningKey) != ed25519.PrivateKeySize {
		return errors.New("the instance signing key must be a 64 byte Ed25519 private key")
	}
	if len(c.IPPepper) < minimumPepperBytes {
		return errors.New("the IP pepper must be at least 16 bytes")
	}
	parsed, err := config.ParsePublicURL(config.EnvPublicURL, c.PublicURL.Origin)
	if err != nil {
		return err
	}
	if parsed != c.PublicURL {
		return errors.New(config.EnvPublicURL + " does not match its parsed host and scheme")
	}
	return nil
}

type server struct {
	cfg         Config
	console     fs.FS
	publicHost  string
	cookieName  string
	publicKey   string
	fingerprint string
	keyID       string
	metrics     *metrics
	forwarded   atomic.Bool
}

func New(cfg Config) http.Handler {
	if err := cfg.Validate(); err != nil {
		panic("server: " + err.Error())
	}
	s := newServer(cfg)
	return s.handler()
}

func newServer(cfg Config) *server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CapabilityTTL <= 0 {
		cfg.CapabilityTTL = 5 * time.Minute
	}
	if cfg.MinimumClientVersion == "" {
		cfg.MinimumClientVersion = "0.0.0"
	}
	if cfg.LatestDesktopVersion == "" {
		cfg.LatestDesktopVersion = cfg.MinimumClientVersion
	}
	if cfg.Updates == nil {
		service, err := updates.New(updates.Options{Store: cfg.Store, Version: cfg.Version})
		if err != nil {
			panic("server: " + err.Error())
		}
		cfg.Updates = service
	}
	raw := cfg.SigningKey.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	if cfg.CapabilityKeyID == "" {
		cfg.CapabilityKeyID = "selfhost-" + fingerprint[:16]
	}
	consoleFS := cfg.Console
	if consoleFS == nil {
		consoleFS = console.FS()
	}
	cookieName := "sesame_owner"
	if cfg.PublicURL.Secure {
		cookieName = "__Host-sesame_owner"
	}
	s := &server{
		cfg:         cfg,
		console:     consoleFS,
		publicHost:  canonicalHost(cfg.PublicURL.Host, cfg.PublicURL.Secure),
		cookieName:  cookieName,
		publicKey:   base64.RawURLEncoding.EncodeToString(raw),
		fingerprint: fingerprint,
		keyID:       cfg.CapabilityKeyID,
		metrics:     newMetrics(),
	}
	return s
}

func (s *server) now() time.Time { return s.cfg.Now() }

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /livez", s.livez)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /config.json", s.consoleConfig)
	mux.HandleFunc("GET /v1/instance", s.instance)
	mux.HandleFunc("GET /v1/capabilities", s.capabilities)
	if s.cfg.Metrics {
		mux.HandleFunc("GET /metrics", s.metricsEndpoint)
	}

	mux.HandleFunc("POST /v1/desktop/link", s.desktopRoute(s.desktopLink))
	mux.HandleFunc("GET /v1/desktop/status", s.desktopRoute(s.desktopStatus))
	mux.HandleFunc("POST /v1/desktop/heartbeat", s.desktopRoute(s.desktopHeartbeat))
	mux.HandleFunc("GET /v1/desktop/config", s.desktopRoute(s.desktopConfig))
	mux.HandleFunc("DELETE /v1/desktop/connection", s.desktopRoute(s.desktopDisconnect))

	mux.HandleFunc("POST /v1/owner/setup/details", s.ownerOpen(s.setupDetails))
	mux.HandleFunc("POST /v1/owner/setup", s.ownerOpen(s.setupComplete))
	mux.HandleFunc("POST /v1/owner/login", s.ownerOpen(s.login))
	mux.HandleFunc("POST /v1/owner/logout", s.owner(s.logout))
	mux.HandleFunc("GET /v1/owner/session", s.owner(s.session))
	mux.HandleFunc("POST /v1/owner/step-up", s.owner(s.stepUp))

	mux.HandleFunc("GET /v1/owner/owners", s.owner(s.listOwners))
	mux.HandleFunc("POST /v1/owner/owners", s.ownerStepUp(s.inviteOwner))
	mux.HandleFunc("DELETE /v1/owner/owners/{id}", s.ownerStepUp(s.removeOwner))

	mux.HandleFunc("GET /v1/owner/members", s.owner(s.listMembers))
	mux.HandleFunc("POST /v1/owner/members", s.owner(s.createMember))
	mux.HandleFunc("PATCH /v1/owner/members/{id}", s.owner(s.renameMember))
	mux.HandleFunc("DELETE /v1/owner/members/{id}", s.ownerStepUp(s.deleteMember))

	mux.HandleFunc("POST /v1/owner/pairings", s.owner(s.createPairing))
	mux.HandleFunc("GET /v1/owner/pairings", s.owner(s.listPairings))
	mux.HandleFunc("DELETE /v1/owner/pairings/{id}", s.owner(s.cancelPairing))

	mux.HandleFunc("GET /v1/owner/devices", s.owner(s.listDevices))
	mux.HandleFunc("DELETE /v1/owner/devices/{id}", s.ownerStepUp(s.revokeDevice))

	mux.HandleFunc("GET /v1/owner/audit", s.owner(s.audit))
	mux.HandleFunc("GET /v1/owner/settings", s.owner(s.getSettings))
	mux.HandleFunc("PATCH /v1/owner/settings", s.ownerStepUp(s.patchSettings))
	mux.HandleFunc("GET /v1/owner/flags", s.owner(s.listFlags))
	mux.HandleFunc("PATCH /v1/owner/flags/{key}", s.owner(s.patchFlag))
	mux.HandleFunc("GET /v1/owner/updates", s.owner(s.getUpdates))
	mux.HandleFunc("PATCH /v1/owner/updates", s.ownerStepUp(s.patchUpdates))
	mux.HandleFunc("POST /v1/owner/updates/check", s.owner(s.checkUpdates))
	mux.HandleFunc("GET /v1/owner/system", s.owner(s.system))
	mux.HandleFunc("POST /v1/owner/export", s.ownerStepUp(s.export))

	mux.HandleFunc("/", s.fallback(mux))
	return s.wrap(mux)
}

func canonicalHost(host string, secure bool) string {
	host = strings.ToLower(strings.TrimSpace(host))
	name, port := host, ""
	if index := strings.LastIndex(host, ":"); index >= 0 && !strings.HasSuffix(host, "]") {
		name, port = host[:index], host[index+1:]
	}
	name = strings.TrimSuffix(strings.Trim(name, "[]"), ".")
	if (secure && port == "443") || (!secure && port == "80") {
		port = ""
	}
	if strings.Contains(name, ":") {
		name = "[" + name + "]"
	}
	if port != "" {
		return name + ":" + port
	}
	return name
}
