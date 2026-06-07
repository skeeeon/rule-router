// file: internal/rule/hmac.go
// Generic HMAC verification for inbound webhooks (GitHub/Shopify-style).
// Pure stdlib crypto (hmac/sha256/sha1, hex/base64) — WASM-safe, no build tag.

package rule

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"net/textproto"
	"strings"
)

// HMAC verification result labels (also used as metric label values).
const (
	hmacValid   = "valid"   // signature present and matches
	hmacInvalid = "invalid" // signature present but does not match (or undecodable)
	hmacMissing = "missing" // no signature header on the request
	hmacError   = "error"   // misconfiguration: empty secret / unknown algorithm or encoding
)

// hmacHash returns the hash constructor for the configured algorithm.
// Defaults to sha256. Returns ok=false for unknown algorithms.
func hmacHash(algorithm string) (func() hash.Hash, bool) {
	switch strings.ToLower(algorithm) {
	case "", "sha256":
		return sha256.New, true
	case "sha1":
		return sha1.New, true
	default:
		return nil, false
	}
}

// hmacDecode decodes a signature string per the configured encoding.
// Defaults to hex. Returns ok=false for unknown encodings.
func hmacDecode(encoding, value string) ([]byte, bool, error) {
	switch strings.ToLower(encoding) {
	case "", "hex":
		b, err := hex.DecodeString(value)
		return b, true, err
	case "base64":
		b, err := base64.StdEncoding.DecodeString(value)
		return b, true, err
	default:
		return nil, false, nil
	}
}

// verifyHMAC verifies the request signature against HMAC(secret, body).
// secret is the already-resolved shared secret. Returns one of the hmac* result
// labels. Only hmacValid means the request is authenticated; every other result
// must be treated as a failure by the caller (fail-closed).
func verifyHMAC(cfg *HMACConfig, secret, body []byte, headers map[string]string) string {
	if len(secret) == 0 {
		return hmacError
	}

	newHash, ok := hmacHash(cfg.Algorithm)
	if !ok {
		return hmacError
	}

	sigValue := headers[textproto.CanonicalMIMEHeaderKey(cfg.Header)]
	if sigValue == "" {
		return hmacMissing
	}
	sigValue = strings.TrimPrefix(sigValue, cfg.Prefix)

	provided, encOK, err := hmacDecode(cfg.Encoding, sigValue)
	if !encOK {
		return hmacError
	}
	if err != nil {
		return hmacInvalid
	}

	mac := hmac.New(newHash, secret)
	mac.Write(body)
	expected := mac.Sum(nil)

	if hmac.Equal(expected, provided) {
		return hmacValid
	}
	return hmacInvalid
}
