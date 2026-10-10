package updates

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"usesesame.app/backend/internal/releases"
)

const (
	FeedSchemaVersion     = 1
	MaxFeedBytes          = 256 << 10
	MaxFeedLifetime       = 45 * 24 * time.Hour
	MaxIssuedSkew         = 10 * time.Minute
	MaxSequence           = 1_000_000_000
	maxURLBytes           = 2048
	maxImagesPerRelease   = 8
	maxBinariesPerRelease = 32
	maxProducts           = 32
	signingContext        = "sesame-update-feed-v1\n"
)

var (
	ErrInvalid  = errors.New("updates: the feed is not valid")
	ErrExpired  = errors.New("updates: the feed has expired")
	ErrRollback = errors.New("updates: the feed is older than one already accepted")

	sha256Pattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]{0,254}$`)
	platformPattern  = regexp.MustCompile(`^[a-z0-9]{2,16}$`)
)

type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
	KeyID     string `json:"keyId"`
}

type Payload struct {
	SchemaVersion int       `json:"schemaVersion"`
	Sequence      int64     `json:"sequence"`
	IssuedAt      time.Time `json:"issuedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	Products      []Product `json:"products"`
}

type Product struct {
	ID       string             `json:"id"`
	Channels map[string]Release `json:"channels"`
}

type Release struct {
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
	NotesURL    string    `json:"notesUrl"`
	Security    bool      `json:"security"`
	MinimumFrom string    `json:"minimumFrom"`
	Images      []Image   `json:"images"`
	Binaries    []Binary  `json:"binaries"`
}

type Image struct {
	Ref string `json:"ref"`
}

type Binary struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type Document struct {
	Payload  Payload
	Envelope Envelope
	Raw      []byte
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

type Verifier struct {
	Keys     Keys
	Registry *Registry
}

func (v Verifier) Open(body []byte) (Document, error) {
	if len(body) > MaxFeedBytes {
		return Document{}, invalid("the feed is larger than %d bytes", MaxFeedBytes)
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil {
		return Document{}, invalid("the envelope is not JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Document{}, invalid("the envelope has data after the JSON object")
	}
	key, known := v.Keys[envelope.KeyID]
	if !known {
		return Document{}, invalid("the key id is not pinned")
	}
	if strings.ContainsAny(envelope.Payload, "\r\n") || strings.ContainsAny(envelope.Signature, "\r\n") {
		return Document{}, invalid("the payload and signature must not hold line breaks")
	}
	payloadBytes, err := base64.RawURLEncoding.Strict().DecodeString(envelope.Payload)
	if err != nil || len(payloadBytes) == 0 {
		return Document{}, invalid("the payload is not unpadded base64url")
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Document{}, invalid("the signature is not a 64 byte Ed25519 signature in unpadded base64url")
	}
	if !ed25519.Verify(key, signedMessage(payloadBytes), signature) {
		return Document{}, invalid("the signature does not match")
	}
	var payload Payload
	payloadDecoder := json.NewDecoder(bytes.NewReader(payloadBytes))
	if err := payloadDecoder.Decode(&payload); err != nil {
		return Document{}, invalid("the payload is not the expected JSON")
	}
	if _, err := payloadDecoder.Token(); !errors.Is(err, io.EOF) {
		return Document{}, invalid("the payload has data after the JSON object")
	}
	if err := ValidatePayload(payload, v.Registry); err != nil {
		return Document{}, err
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return Document{}, err
	}
	return Document{Payload: payload, Envelope: envelope, Raw: canonical}, nil
}

func (v Verifier) Verify(body []byte, now time.Time) (Document, error) {
	document, err := v.Open(body)
	if err != nil {
		return Document{}, err
	}
	if err := CheckFresh(document.Payload, now); err != nil {
		return Document{}, err
	}
	return document, nil
}

func CheckFresh(payload Payload, now time.Time) error {
	if payload.IssuedAt.After(now.Add(MaxIssuedSkew)) {
		return invalid("the feed was issued in the future")
	}
	if now.After(payload.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

func signedMessage(payload []byte) []byte {
	message := make([]byte, 0, len(signingContext)+len(payload))
	message = append(message, signingContext...)
	return append(message, payload...)
}

func Sign(payload Payload, keyID string, key ed25519.PrivateKey) ([]byte, error) {
	if !ValidKeyID(keyID) {
		return nil, errors.New("updates: the key id is not valid")
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("updates: the private key is not an Ed25519 key")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(key, signedMessage(payloadBytes))
	return json.Marshal(Envelope{
		Payload:   base64.RawURLEncoding.EncodeToString(payloadBytes),
		Signature: base64.RawURLEncoding.EncodeToString(signature),
		KeyID:     keyID,
	})
}

func ValidatePayload(payload Payload, registry *Registry) error {
	if payload.SchemaVersion != FeedSchemaVersion {
		return invalid("schemaVersion must be %d", FeedSchemaVersion)
	}
	if payload.Sequence < 1 || payload.Sequence > MaxSequence {
		return invalid("sequence must be a whole number from 1 to %d", MaxSequence)
	}
	if payload.IssuedAt.IsZero() || payload.ExpiresAt.IsZero() {
		return invalid("issuedAt and expiresAt are required")
	}
	if !payload.ExpiresAt.After(payload.IssuedAt) || payload.ExpiresAt.Sub(payload.IssuedAt) > MaxFeedLifetime {
		return invalid("expiresAt must be after issuedAt and at most 45 days later")
	}
	if len(payload.Products) == 0 || len(payload.Products) > maxProducts {
		return invalid("the feed must list between 1 and %d products", maxProducts)
	}
	seen := map[string]bool{}
	for _, product := range payload.Products {
		if !validProductID(product.ID) {
			return invalid("a product id is not valid")
		}
		if seen[product.ID] {
			return invalid("product %q appears more than once", product.ID)
		}
		seen[product.ID] = true
		spec, registered := registry.Lookup(product.ID)
		if !registered {
			continue
		}
		for channel, release := range product.Channels {
			if !spec.HasChannel(channel) {
				continue
			}
			if err := validateRelease(release); err != nil {
				return invalid("product %q channel %q: %v", product.ID, channel, err)
			}
		}
	}
	for _, id := range registry.IDs() {
		if !seen[id] {
			return invalid("product %q is missing", id)
		}
	}
	return nil
}

func validateRelease(release Release) error {
	if _, err := releases.ParseVersion(release.Version); err != nil {
		return errors.New("version is not canonical SemVer")
	}
	if release.PublishedAt.IsZero() {
		return errors.New("publishedAt is required")
	}
	if err := validateURL(release.NotesURL); err != nil {
		return fmt.Errorf("notesUrl: %w", err)
	}
	if release.MinimumFrom != "" && !releases.ValidVersion(release.MinimumFrom) {
		return errors.New("minimumFrom is not canonical SemVer")
	}
	if len(release.Images) > maxImagesPerRelease {
		return errors.New("too many images")
	}
	for _, image := range release.Images {
		if !ValidImageRef(image.Ref) {
			return errors.New("an image ref must be a lowercase name followed by @sha256: and 64 hex digits")
		}
	}
	if len(release.Binaries) > maxBinariesPerRelease {
		return errors.New("too many binaries")
	}
	platforms := map[string]bool{}
	for _, binary := range release.Binaries {
		if !platformPattern.MatchString(binary.OS) || !platformPattern.MatchString(binary.Arch) {
			return errors.New("a binary needs a plain os and arch")
		}
		if platforms[binary.OS+"/"+binary.Arch] {
			return fmt.Errorf("binary %s/%s is listed twice", binary.OS, binary.Arch)
		}
		platforms[binary.OS+"/"+binary.Arch] = true
		if err := validateURL(binary.URL); err != nil {
			return fmt.Errorf("binary url: %w", err)
		}
		if !sha256Pattern.MatchString(binary.SHA256) {
			return errors.New("a binary sha256 must be 64 lowercase hex digits")
		}
	}
	return nil
}

func ValidImageRef(ref string) bool {
	name, digest, found := strings.Cut(ref, "@sha256:")
	return found && imageNamePattern.MatchString(name) && sha256Pattern.MatchString(digest)
}

func validateURL(raw string) error {
	if raw == "" || len(raw) > maxURLBytes {
		return errors.New("a url is required and must be at most 2048 bytes")
	}
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		if character <= 0x20 || character >= 0x7f || strings.IndexByte("'\"`\\<>{}[]|^", character) >= 0 {
			return errors.New("the url holds a character that is not allowed")
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return errors.New("the url must be https with a host, no user info and no fragment")
	}
	return nil
}

func (p Payload) Release(productID, channel string) (Release, bool) {
	for _, product := range p.Products {
		if product.ID == productID {
			release, found := product.Channels[channel]
			return release, found
		}
	}
	return Release{}, false
}
