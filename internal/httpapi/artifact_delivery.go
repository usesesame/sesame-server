package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Object storage must reject every request lacking a valid expiry and HMAC signature; public buckets are not supported.
type ArtifactDelivery interface {
	SignedURL(context.Context, string, time.Time) (string, error)
}

type HMACArtifactDelivery struct {
	baseURL *url.URL
	key     []byte
}

func NewHMACArtifactDelivery(rawBaseURL string, key []byte) (*HMACArtifactDelivery, error) {
	if len(key) != 32 {
		return nil, errors.New("artifact gateway signing key must contain 32 bytes")
	}
	baseURL, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("artifact gateway URL must be a credential-free HTTPS origin without query or fragment")
	}
	return &HMACArtifactDelivery{baseURL: baseURL, key: append([]byte(nil), key...)}, nil
}

func (delivery *HMACArtifactDelivery) SignedURL(_ context.Context, objectKey string, expiresAt time.Time) (string, error) {
	if delivery == nil || !validArtifactObjectKey(objectKey) || expiresAt.Before(time.Now().UTC()) {
		return "", errors.New("artifact delivery request is invalid")
	}
	expires := expiresAt.UTC().Unix()
	mac := hmac.New(sha256.New, delivery.key)
	mac.Write([]byte("sesame-artifact-gateway-v1\n" + objectKey + "\n" + strconv.FormatInt(expires, 10)))
	result := *delivery.baseURL
	result.Path = strings.TrimRight(delivery.baseURL.Path, "/") + "/" + objectKey
	query := url.Values{}
	query.Set("expires", strconv.FormatInt(expires, 10))
	query.Set("signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	result.RawQuery = query.Encode()
	return result.String(), nil
}
