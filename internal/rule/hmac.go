// HMAC verification for inbound webhooks. The generic scheme covers providers
// that sign the raw body (GitHub, Shopify, …); named schemes cover the
// timestamp-signed formats of Stripe, Slack, and Standard Webhooks.
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
	"strconv"
	"strings"
	"time"
)

// HMAC verification result labels (also used as metric label values).
const (
	hmacValid   = "valid"   // signature present and matches
	hmacInvalid = "invalid" // signature present but does not match (or undecodable)
	hmacMissing = "missing" // no signature header on the request
	hmacExpired = "expired" // signature matches but its timestamp is outside the tolerance
	hmacError   = "error"   // misconfiguration: empty secret / unknown algorithm or encoding
)

// Named signature schemes. An empty scheme is the generic HMAC(secret, body).
const (
	schemeStripe           = "stripe"
	schemeSlack            = "slack"
	schemeStandardWebhooks = "standardwebhooks"
)

// signatureTolerance is how far a timestamped scheme's timestamp may be from
// now, in either direction. Five minutes is what Stripe and Slack recommend.
const signatureTolerance = 5 * time.Minute

// isHMACScheme reports whether scheme is a supported value ("" = generic).
func isHMACScheme(scheme string) bool {
	switch scheme {
	case "", schemeStripe, schemeSlack, schemeStandardWebhooks:
		return true
	}
	return false
}

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

// verifyHMAC verifies the request signature per cfg.Scheme. secret is the
// already-resolved shared secret; now is the current time, used only by the
// timestamped schemes. Returns one of the hmac* result labels. Only hmacValid
// means the request is authenticated; every other result must be treated as a
// failure by the caller (fail-closed).
func verifyHMAC(cfg *HMACConfig, secret, body []byte, headers map[string]string, now time.Time) string {
	if len(secret) == 0 {
		return hmacError
	}

	switch cfg.Scheme {
	case "":
		return verifyGeneric(cfg, secret, body, headers)
	case schemeStripe:
		return verifyStripe(secret, body, headers, now)
	case schemeSlack:
		return verifySlack(secret, body, headers, now)
	case schemeStandardWebhooks:
		return verifyStandardWebhooks(secret, body, headers, now)
	default:
		return hmacError
	}
}

// verifyGeneric checks HMAC(secret, body) against a single configured header.
func verifyGeneric(cfg *HMACConfig, secret, body []byte, headers map[string]string) string {
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

// verifyStripe checks a Stripe-Signature header: "t=<ts>,v1=<hex>[,v1=<hex>…]".
// Signed content is "{t}.{body}". Only v1 signatures count; v0 (test-mode) and
// any other scheme are ignored, as Stripe's docs require. Several v1 values
// appear while a secret is being rolled; any one may match.
func verifyStripe(secret, body []byte, headers map[string]string, now time.Time) string {
	header := headers["Stripe-Signature"]
	if header == "" {
		return hmacMissing
	}

	var ts string
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "t":
			ts = value
		case "v1":
			if sig, err := hex.DecodeString(value); err == nil {
				sigs = append(sigs, sig)
			}
		}
	}
	if ts == "" || len(sigs) == 0 {
		return hmacInvalid
	}

	expected := signSHA256(secret, []byte(ts), []byte("."), body)
	return checkSignedTimestamp(expected, sigs, ts, now)
}

// verifySlack checks X-Slack-Signature ("v0=<hex>") over "v0:{ts}:{body}",
// with the timestamp from X-Slack-Request-Timestamp.
func verifySlack(secret, body []byte, headers map[string]string, now time.Time) string {
	ts := headers["X-Slack-Request-Timestamp"]
	sigValue := headers["X-Slack-Signature"]
	if ts == "" || sigValue == "" {
		return hmacMissing
	}

	hexSig, ok := strings.CutPrefix(sigValue, "v0=")
	if !ok {
		return hmacInvalid
	}
	sig, err := hex.DecodeString(hexSig)
	if err != nil {
		return hmacInvalid
	}

	expected := signSHA256(secret, []byte("v0:"), []byte(ts), []byte(":"), body)
	return checkSignedTimestamp(expected, [][]byte{sig}, ts, now)
}

// verifyStandardWebhooks checks the Standard Webhooks spec: Webhook-Signature
// holds space-separated "v1,<base64>" entries, signed over "{id}.{ts}.{body}".
// The secret is "whsec_<base64>"; the HMAC key is the decoded bytes. Entries
// with another version (e.g. "v1a," for Ed25519) are ignored.
func verifyStandardWebhooks(secret, body []byte, headers map[string]string, now time.Time) string {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(secret), "whsec_"))
	if err != nil || len(key) == 0 {
		return hmacError
	}

	id := headers["Webhook-Id"]
	ts := headers["Webhook-Timestamp"]
	sigValue := headers["Webhook-Signature"]
	if id == "" || ts == "" || sigValue == "" {
		return hmacMissing
	}

	var sigs [][]byte
	for _, entry := range strings.Fields(sigValue) {
		b64, ok := strings.CutPrefix(entry, "v1,")
		if !ok {
			continue
		}
		if sig, err := base64.StdEncoding.DecodeString(b64); err == nil {
			sigs = append(sigs, sig)
		}
	}
	if len(sigs) == 0 {
		return hmacInvalid
	}

	expected := signSHA256(key, []byte(id), []byte("."), []byte(ts), []byte("."), body)
	return checkSignedTimestamp(expected, sigs, ts, now)
}

// signSHA256 returns HMAC-SHA256(secret, parts...) without concatenating the
// parts into a copy of the body.
func signSHA256(secret []byte, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, secret)
	for _, p := range parts {
		mac.Write(p)
	}
	return mac.Sum(nil)
}

// checkSignedTimestamp is the shared tail of the timestamped schemes: the
// request is valid if any candidate signature matches AND its timestamp is
// fresh. The signature is checked first, so a forged request with an old
// timestamp reports invalid, not expired — expired means "really from the
// provider, but stale" (clock skew, or a replayed/delayed delivery).
func checkSignedTimestamp(expected []byte, candidates [][]byte, ts string, now time.Time) string {
	matched := false
	for _, sig := range candidates {
		if hmac.Equal(expected, sig) {
			matched = true
			break
		}
	}
	if !matched {
		return hmacInvalid
	}
	return checkTimestamp(ts, now)
}

// checkTimestamp parses a unix-seconds timestamp and checks it is within
// signatureTolerance of now, in either direction.
func checkTimestamp(ts string, now time.Time) string {
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return hmacInvalid
	}
	diff := now.Sub(time.Unix(secs, 0))
	if diff > signatureTolerance || diff < -signatureTolerance {
		return hmacExpired
	}
	return hmacValid
}
