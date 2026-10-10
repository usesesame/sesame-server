package updates

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidFeedVerifies(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	body := signed(t, samplePayload(), pair)
	document, err := verifierFor(pair).Verify(body, testNow)
	if err != nil {
		t.Fatal(err)
	}
	release, found := document.Payload.Release(ServerProductID, "stable")
	if !found || release.Version != "0.2.0" || document.Payload.Sequence != 5 || !release.Security || release.MinimumFrom != "0.1.0" {
		t.Fatalf("document = %+v", document.Payload)
	}
	if len(release.Images) != 1 || len(release.Binaries) != 2 || document.Envelope.KeyID != testKeyID {
		t.Fatalf("release = %+v", release)
	}
	if _, found := document.Payload.Release(ServerProductID, "nightly"); found {
		t.Fatal("an unlisted channel was found")
	}
}

func TestSignMatchesTheDocumentedFormat(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	payload := samplePayload()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(signed(t, payload, pair)) != string(manualEnvelope(t, encoded, pair)) {
		t.Fatal("Sign does not produce an envelope over the context prefix and the payload bytes")
	}
}

func TestSignatureNeedsTheSigningContext(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	payload, err := json.Marshal(samplePayload())
	if err != nil {
		t.Fatal(err)
	}
	bare := ed25519.Sign(pair.private, payload)
	body, err := json.Marshal(Envelope{Payload: base64.RawURLEncoding.EncodeToString(payload), Signature: base64.RawURLEncoding.EncodeToString(bare), KeyID: pair.id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifierFor(pair).Verify(body, testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a signature over the bare payload err = %v", err)
	}
}

func TestTamperedPayloadIsRejected(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	body := signed(t, samplePayload(), pair)
	var envelope Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	original, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(original), `"version":"0.2.0"`, `"version":"9.9.9"`, 1)
	if changed == string(original) {
		t.Fatal("the test payload does not hold the expected version text")
	}
	envelope.Payload = base64.RawURLEncoding.EncodeToString([]byte(changed))
	tampered, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifierFor(pair).Verify(tampered, testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered payload err = %v", err)
	}
	envelope.Payload = base64.RawURLEncoding.EncodeToString(original)
	envelope.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	zeroed, _ := json.Marshal(envelope)
	if _, err := verifierFor(pair).Verify(zeroed, testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero signature err = %v", err)
	}
}

func TestWrongKeyAndUnknownKeyIDAreRejected(t *testing.T) {
	pinned := newKeyPair(t, testKeyID)
	attacker := newKeyPair(t, testKeyID)
	if _, err := verifierFor(pinned).Verify(signed(t, samplePayload(), attacker), testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a feed signed by another key under the pinned id err = %v", err)
	}
	other := newKeyPair(t, "unpinned-key")
	if _, err := verifierFor(pinned).Verify(signed(t, samplePayload(), other), testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a feed with an unknown key id err = %v", err)
	}
	empty := Verifier{Keys: Keys{}, Registry: DefaultRegistry()}
	if _, err := empty.Verify(signed(t, samplePayload(), pinned), testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a verifier with no keys accepted a feed: %v", err)
	}
}

func TestSecondPinnedKeyIsAccepted(t *testing.T) {
	first, second := newKeyPair(t, "key-a"), newKeyPair(t, "key-b")
	verifier := Verifier{Keys: Keys{first.id: first.public, second.id: second.public}, Registry: DefaultRegistry()}
	for _, pair := range []keyPair{first, second} {
		if _, err := verifier.Verify(signed(t, samplePayload(), pair), testNow); err != nil {
			t.Fatalf("%s: %v", pair.id, err)
		}
	}
}

func TestFreshnessWindow(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	verifier := verifierFor(pair)
	payload := samplePayload()
	if _, err := verifier.Verify(signed(t, payload, pair), payload.ExpiresAt.Add(time.Second)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired feed err = %v", err)
	}
	if _, err := verifier.Verify(signed(t, payload, pair), payload.ExpiresAt); err != nil {
		t.Fatalf("a feed is valid until its expiry: %v", err)
	}
	future := payload.with(func(p *Payload) {
		p.IssuedAt = testNow.Add(MaxIssuedSkew + time.Second)
		p.ExpiresAt = p.IssuedAt.Add(24 * time.Hour)
	})
	if _, err := verifier.Verify(signed(t, future, pair), testNow); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrExpired) {
		t.Fatalf("a feed issued in the future err = %v", err)
	}
	nearFuture := payload.with(func(p *Payload) {
		p.IssuedAt = testNow.Add(MaxIssuedSkew)
		p.ExpiresAt = p.IssuedAt.Add(24 * time.Hour)
	})
	if _, err := verifier.Verify(signed(t, nearFuture, pair), testNow); err != nil {
		t.Fatalf("ten minutes of clock skew is allowed: %v", err)
	}
	if _, err := verifier.Open(signed(t, payload, pair)); err != nil {
		t.Fatalf("Open must not check freshness: %v", err)
	}
}

func TestLifetimeLimit(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	verifier := verifierFor(pair)
	limit := samplePayload().with(func(p *Payload) { p.ExpiresAt = p.IssuedAt.Add(MaxFeedLifetime) })
	if _, err := verifier.Verify(signed(t, limit, pair), testNow); err != nil {
		t.Fatalf("45 days is allowed: %v", err)
	}
	over := samplePayload().with(func(p *Payload) { p.ExpiresAt = p.IssuedAt.Add(MaxFeedLifetime + time.Second) })
	if _, err := verifier.Verify(signed(t, over, pair), testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("more than 45 days err = %v", err)
	}
	backwards := samplePayload().with(func(p *Payload) { p.ExpiresAt = p.IssuedAt })
	if _, err := verifier.Verify(signed(t, backwards, pair), testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expiry equal to issue err = %v", err)
	}
}

func TestMalformedPayloadsAreRejected(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	verifier := verifierFor(pair)
	cases := map[string]Payload{
		"schema 2":                samplePayload().with(func(p *Payload) { p.SchemaVersion = 2 }),
		"schema 0":                samplePayload().with(func(p *Payload) { p.SchemaVersion = 0 }),
		"sequence 0":              samplePayload().with(func(p *Payload) { p.Sequence = 0 }),
		"negative sequence":       samplePayload().with(func(p *Payload) { p.Sequence = -3 }),
		"no issuedAt":             samplePayload().with(func(p *Payload) { p.IssuedAt = time.Time{} }),
		"no products":             samplePayload().with(func(p *Payload) { p.Products = nil }),
		"wrong product":           samplePayload().with(func(p *Payload) { p.Products[0].ID = "other-product" }),
		"duplicate product":       samplePayload().with(func(p *Payload) { p.Products = append(p.Products, p.Products[0]) }),
		"product id with capital": samplePayload().with(func(p *Payload) { p.Products = append(p.Products, Product{ID: "Other"}) }),
		"version with v":          samplePayload().withStable(func(r *Release) { r.Version = "v0.2.0" }),
		"version with two parts":  samplePayload().withStable(func(r *Release) { r.Version = "0.2" }),
		"version with build":      samplePayload().withStable(func(r *Release) { r.Version = "0.2.0+build" }),
		"version empty":           samplePayload().withStable(func(r *Release) { r.Version = "" }),
		"minimumFrom bad":         samplePayload().withStable(func(r *Release) { r.MinimumFrom = "latest" }),
		"no publishedAt":          samplePayload().withStable(func(r *Release) { r.PublishedAt = time.Time{} }),
		"notes over http":         samplePayload().withStable(func(r *Release) { r.NotesURL = "http://example.net/notes" }),
		"notes empty":             samplePayload().withStable(func(r *Release) { r.NotesURL = "" }),
		"notes with user info":    samplePayload().withStable(func(r *Release) { r.NotesURL = "https://user:pass@example.net/notes" }),
		"notes with fragment":     samplePayload().withStable(func(r *Release) { r.NotesURL = "https://example.net/notes#x" }),
		"notes with quote":        samplePayload().withStable(func(r *Release) { r.NotesURL = "https://example.net/a'b" }),
		"notes with space":        samplePayload().withStable(func(r *Release) { r.NotesURL = "https://example.net/a b" }),
		"notes with newline":      samplePayload().withStable(func(r *Release) { r.NotesURL = "https://example.net/a\nb" }),
		"notes javascript":        samplePayload().withStable(func(r *Release) { r.NotesURL = "javascript:alert(1)" }),
		"notes without host":      samplePayload().withStable(func(r *Release) { r.NotesURL = "https:///path" }),
		"binary over http":        samplePayload().withStable(func(r *Release) { r.Binaries[0].URL = "http://downloads.example.net/sesame-server" }),
		"binary file url":         samplePayload().withStable(func(r *Release) { r.Binaries[0].URL = "file:///tmp/sesame-server" }),
		"binary url with user":    samplePayload().withStable(func(r *Release) { r.Binaries[0].URL = "https://a@downloads.example.net/x" }),
		"binary url with shell":   samplePayload().withStable(func(r *Release) { r.Binaries[0].URL = "https://downloads.example.net/x`id`" }),
		"binary sha too short":    samplePayload().withStable(func(r *Release) { r.Binaries[0].SHA256 = testSHA[:63] }),
		"binary sha too long":     samplePayload().withStable(func(r *Release) { r.Binaries[0].SHA256 = testSHA + "b" }),
		"binary sha uppercase":    samplePayload().withStable(func(r *Release) { r.Binaries[0].SHA256 = strings.ToUpper(testSHA) }),
		"binary sha not hex":      samplePayload().withStable(func(r *Release) { r.Binaries[0].SHA256 = repeat("g", 64) }),
		"binary sha empty":        samplePayload().withStable(func(r *Release) { r.Binaries[0].SHA256 = "" }),
		"binary os with slash":    samplePayload().withStable(func(r *Release) { r.Binaries[0].OS = "li/nux" }),
		"binary arch empty":       samplePayload().withStable(func(r *Release) { r.Binaries[0].Arch = "" }),
		"binary listed twice":     samplePayload().withStable(func(r *Release) { r.Binaries = append(r.Binaries, r.Binaries[0]) }),
		"image without digest":    samplePayload().withStable(func(r *Release) { r.Images[0].Ref = "registry.example.net/sesame/server:0.2.0" }),
		"image digest too short":  samplePayload().withStable(func(r *Release) { r.Images[0].Ref = "registry.example.net/sesame/server@sha256:" + testDigest[:63] }),
		"image digest uppercase": samplePayload().withStable(func(r *Release) {
			r.Images[0].Ref = "registry.example.net/sesame/server@sha256:" + strings.ToUpper(testDigest)
		}),
		"image digest md5":      samplePayload().withStable(func(r *Release) { r.Images[0].Ref = "registry.example.net/sesame/server@md5:" + testDigest }),
		"image name empty":      samplePayload().withStable(func(r *Release) { r.Images[0].Ref = "@sha256:" + testDigest }),
		"image name with space": samplePayload().withStable(func(r *Release) { r.Images[0].Ref = "registry.example.net/a b@sha256:" + testDigest }),
		"image name with shell": samplePayload().withStable(func(r *Release) { r.Images[0].Ref = "registry.example.net/a;rm@sha256:" + testDigest }),
		"image name uppercase":  samplePayload().withStable(func(r *Release) { r.Images[0].Ref = "Registry.Example.net/a@sha256:" + testDigest }),
		"image ref empty":       samplePayload().withStable(func(r *Release) { r.Images[0].Ref = "" }),
		"too many images": samplePayload().withStable(func(r *Release) {
			r.Images = make([]Image, 9)
			for i := range r.Images {
				r.Images[i] = Image{Ref: "a/b@sha256:" + testDigest}
			}
		}),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(signed(t, payload, pair), testNow); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want an invalid feed", err)
			}
		})
	}
}

func TestUnregisteredProductsAndChannelsAreIgnored(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	payload := samplePayload().with(func(p *Payload) {
		p.Products = append(p.Products, Product{ID: "sesame-relay", Channels: map[string]Release{"stable": {Version: "not a version"}}})
		p.Products[0].Channels["nightly"] = Release{Version: "garbage"}
	})
	if _, err := verifierFor(pair).Verify(signed(t, payload, pair), testNow); err != nil {
		t.Fatalf("unregistered products and channels must not break verification: %v", err)
	}
}

func TestRegisteredProductsAreValidatedAndRequired(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	registry := DefaultRegistry()
	if err := registry.Register(ProductSpec{ID: "sesame-relay", Channels: []string{"stable"}}); err != nil {
		t.Fatal(err)
	}
	verifier := Verifier{Keys: pair.keys(), Registry: registry}
	if _, err := verifier.Verify(signed(t, samplePayload(), pair), testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a feed without a registered product err = %v", err)
	}
	both := samplePayload().with(func(p *Payload) {
		p.Products = append(p.Products, Product{ID: "sesame-relay", Channels: map[string]Release{"stable": {
			Version: "1.0.0", PublishedAt: testNow, NotesURL: "https://example.net/relay",
		}}})
	})
	if _, err := verifier.Verify(signed(t, both, pair), testNow); err != nil {
		t.Fatal(err)
	}
	broken := both.with(func(p *Payload) {
		release := p.Products[1].Channels["stable"]
		release.NotesURL = "http://example.net/relay"
		p.Products[1].Channels["stable"] = release
	})
	if _, err := verifier.Verify(signed(t, broken, pair), testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an invalid release of a registered product err = %v", err)
	}
}

func TestRegistryRejectsBadAndDuplicateProducts(t *testing.T) {
	registry := NewRegistry()
	for _, spec := range []ProductSpec{
		{ID: "", Channels: []string{"stable"}},
		{ID: "Upper", Channels: []string{"stable"}},
		{ID: "-lead", Channels: []string{"stable"}},
		{ID: "ok", Channels: nil},
		{ID: "ok", Channels: []string{"Bad Channel"}},
	} {
		if err := registry.Register(spec); err == nil {
			t.Fatalf("%+v was accepted", spec)
		}
	}
	if err := registry.Register(ProductSpec{ID: "ok", Channels: []string{"stable"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(ProductSpec{ID: "ok", Channels: []string{"stable"}}); err == nil {
		t.Fatal("a product registered twice")
	}
	if spec, found := registry.Lookup("ok"); !found || !spec.HasChannel("stable") || spec.HasChannel("beta") {
		t.Fatalf("lookup = %+v %v", spec, found)
	}
	if _, found := DefaultRegistry().Lookup(ServerProductID); !found {
		t.Fatal("the default registry lacks the server product")
	}
}

func TestMalformedEnvelopesAreRejected(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	verifier := verifierFor(pair)
	good := signed(t, samplePayload(), pair)
	var envelope Envelope
	if err := json.Unmarshal(good, &envelope); err != nil {
		t.Fatal(err)
	}
	payloadBytes, _ := base64.RawURLEncoding.DecodeString(envelope.Payload)
	build := func(change func(*Envelope)) []byte {
		copied := envelope
		change(&copied)
		body, _ := json.Marshal(copied)
		return body
	}
	cases := map[string][]byte{
		"empty":                 nil,
		"not json":              []byte("<html>"),
		"array":                 []byte("[]"),
		"trailing data":         append(append([]byte{}, good...), []byte(" {}")...),
		"padded payload":        build(func(e *Envelope) { e.Payload = base64.URLEncoding.EncodeToString(payloadBytes) + "=" }),
		"standard alphabet":     build(func(e *Envelope) { e.Payload = base64.StdEncoding.EncodeToString(append(payloadBytes, 0xfb, 0xff)) }),
		"empty payload":         build(func(e *Envelope) { e.Payload = "" }),
		"short signature":       build(func(e *Envelope) { e.Signature = e.Signature[:20] }),
		"signature not base64":  build(func(e *Envelope) { e.Signature = "***" }),
		"missing key id":        build(func(e *Envelope) { e.KeyID = "" }),
		"payload not json":      manualEnvelope(t, []byte("not json"), pair),
		"payload trailing data": manualEnvelope(t, append(append([]byte{}, payloadBytes...), []byte("{}")...), pair),
		"payload array":         manualEnvelope(t, []byte("[]"), pair),
		"fractional sequence":   manualEnvelope(t, []byte(strings.Replace(string(payloadBytes), `"sequence":5`, `"sequence":5.5`, 1)), pair),
		"string sequence":       manualEnvelope(t, []byte(strings.Replace(string(payloadBytes), `"sequence":5`, `"sequence":"5"`, 1)), pair),
		"huge sequence":         manualEnvelope(t, []byte(strings.Replace(string(payloadBytes), `"sequence":5`, `"sequence":99999999999999999999`, 1)), pair),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(body, testNow); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want an invalid feed", err)
			}
		})
	}
}

func TestOversizeFeedIsRejectedBeforeParsing(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	body := append(signed(t, samplePayload(), pair), []byte(strings.Repeat(" ", MaxFeedBytes))...)
	if _, err := verifierFor(pair).Verify(body, testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize feed err = %v", err)
	}
	exact := signed(t, samplePayload(), pair)
	padded := append(exact, []byte(strings.Repeat(" ", MaxFeedBytes-len(exact)))...)
	if len(padded) != MaxFeedBytes {
		t.Fatalf("padded length = %d", len(padded))
	}
	if _, err := verifierFor(pair).Verify(padded, testNow); err != nil {
		t.Fatalf("a feed of exactly the limit is allowed: %v", err)
	}
}

func TestSignRefusesBadInputs(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	if _, err := Sign(samplePayload(), "bad id", pair.private); err == nil {
		t.Fatal("a key id with a space was accepted")
	}
	if _, err := Sign(samplePayload(), testKeyID, pair.private[:10]); err == nil {
		t.Fatal("a short private key was accepted")
	}
}

func TestParseKeys(t *testing.T) {
	pair := newKeyPair(t, "key-a")
	second := newKeyPair(t, "key-b")
	list := FormatKey(pair.id, pair.public) + ", " + FormatKey(second.id, second.public)
	keys, err := ParseKeys(list)
	if err != nil || len(keys) != 2 || !keys["key-a"].Equal(pair.public) {
		t.Fatalf("keys = %v, err = %v", keys, err)
	}
	if keys, err := ParseKeys("  "); err != nil || len(keys) != 0 {
		t.Fatalf("blank list = %v, %v", keys, err)
	}
	bad := []string{
		"nocolon",
		":" + base64.RawURLEncoding.EncodeToString(pair.public),
		"bad id:" + base64.RawURLEncoding.EncodeToString(pair.public),
		"key-a:" + base64.RawURLEncoding.EncodeToString(pair.public) + "=",
		"key-a:" + base64.RawURLEncoding.EncodeToString(pair.public[:31]),
		"key-a:***",
		FormatKey("key-a", pair.public) + "," + FormatKey("key-a", second.public),
		FormatKey("key-a", pair.public) + ",",
	}
	for _, list := range bad {
		if _, err := ParseKeys(list); err == nil {
			t.Fatalf("%q was accepted", list)
		}
	}
}
