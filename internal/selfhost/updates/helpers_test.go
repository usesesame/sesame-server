package updates

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

const (
	testDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSHA    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testKeyID  = "test-key-1"
)

type keyPair struct {
	id      string
	public  ed25519.PublicKey
	private ed25519.PrivateKey
}

func newKeyPair(t testing.TB, id string) keyPair {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return keyPair{id: id, public: public, private: private}
}

func (k keyPair) keys() Keys { return Keys{k.id: k.public} }

func samplePayload() Payload {
	return Payload{
		SchemaVersion: 1,
		Sequence:      5,
		IssuedAt:      testNow.Add(-time.Hour),
		ExpiresAt:     testNow.Add(30 * 24 * time.Hour),
		Products: []Product{{
			ID: ServerProductID,
			Channels: map[string]Release{
				"stable": {
					Version:     "0.2.0",
					PublishedAt: testNow.Add(-48 * time.Hour),
					NotesURL:    "https://example.net/notes/0.2.0",
					Security:    true,
					MinimumFrom: "0.1.0",
					Images:      []Image{{Ref: "registry.example.net/sesame/server@sha256:" + testDigest}},
					Binaries: []Binary{
						{OS: "linux", Arch: "amd64", URL: "https://downloads.example.net/sesame-server-linux-amd64", SHA256: testSHA},
						{OS: "darwin", Arch: "arm64", URL: "https://downloads.example.net/sesame-server-darwin-arm64", SHA256: testSHA},
					},
				},
				"beta": {
					Version:     "0.3.0-beta.1",
					PublishedAt: testNow.Add(-24 * time.Hour),
					NotesURL:    "https://example.net/notes/0.3.0-beta.1",
				},
			},
		}},
	}
}

func (p Payload) with(change func(*Payload)) Payload {
	copied := p
	copied.Products = make([]Product, len(p.Products))
	for index, product := range p.Products {
		channels := make(map[string]Release, len(product.Channels))
		for name, release := range product.Channels {
			channels[name] = release
		}
		copied.Products[index] = Product{ID: product.ID, Channels: channels}
	}
	change(&copied)
	return copied
}

func (p Payload) withStable(change func(*Release)) Payload {
	return p.with(func(payload *Payload) {
		release := payload.Products[0].Channels["stable"]
		change(&release)
		payload.Products[0].Channels["stable"] = release
	})
}

func manualEnvelope(t testing.TB, payload []byte, pair keyPair) []byte {
	t.Helper()
	signature := ed25519.Sign(pair.private, append([]byte("sesame-update-feed-v1\n"), payload...))
	body, err := json.Marshal(struct {
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
		KeyID     string `json:"keyId"`
	}{base64.RawURLEncoding.EncodeToString(payload), base64.RawURLEncoding.EncodeToString(signature), pair.id})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signed(t testing.TB, payload Payload, pair keyPair) []byte {
	t.Helper()
	body, err := Sign(payload, pair.id, pair.private)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func verifierFor(pair keyPair) Verifier {
	return Verifier{Keys: pair.keys(), Registry: DefaultRegistry()}
}

type timer struct {
	at time.Time
	ch chan time.Time
}

type fakeClock struct {
	mu         sync.Mutex
	now        time.Time
	timers     []timer
	registered chan time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: testNow, registered: make(chan time.Duration, 32)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, timer{at: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	c.registered <- d
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	remaining := c.timers[:0]
	for _, pending := range c.timers {
		if !pending.at.After(c.now) {
			pending.ch <- c.now
			continue
		}
		remaining = append(remaining, pending)
	}
	c.timers = remaining
}

func (c *fakeClock) waitForTimer(t testing.TB) time.Duration {
	t.Helper()
	select {
	case d := <-c.registered:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the scheduler did not wait on the clock")
		return 0
	}
}

func repeat(character string, count int) string { return strings.Repeat(character, count) }
