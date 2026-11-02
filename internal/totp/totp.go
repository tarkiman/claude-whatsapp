// Package totp implements time-based one-time passwords (RFC 6238, built on
// the HOTP algorithm in RFC 4226) using only the standard library — the same
// 6-digit, 30-second, HMAC-SHA1 scheme every common authenticator app
// (Google Authenticator, Authy, 1Password, etc.) expects by default. SHA-1
// here is not a cryptographic weakness in this context: it is simply the
// algorithm the standard specifies and every app implements, and this
// package uses it for nothing else.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	codeDigits = 6
	stepSecs   = 30
	secretLen  = 20 // 160 bits — RFC 4226's recommended HMAC-SHA1 key size

	// SkewSteps allows the code from one step before/after the current one,
	// so modest clock drift between this machine and the operator's phone
	// doesn't lock them out — each step is 30s, so this is a ±30s window.
	SkewSteps = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a fresh random shared secret, base32-encoded
// (RFC 4648, no padding) — the form every authenticator app expects for
// manual entry.
func GenerateSecret() (string, error) {
	b := make([]byte, secretLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// URI builds the otpauth:// enrollment link an authenticator app can follow
// directly — e.g. tapped on the same device the admin page is open on,
// without needing to scan anything.
func URI(secret, accountName, issuer string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(accountName)
	q := url.Values{
		"secret":    {secret},
		"issuer":    {issuer},
		"algorithm": {"SHA1"},
		"digits":    {strconv.Itoa(codeDigits)},
		"period":    {strconv.Itoa(stepSecs)},
	}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// hotp computes one RFC 4226 code for secret at counter.
func hotp(secret []byte, counter uint64) string {
	var buf [8]byte
	for i := 7; i >= 0; i-- {
		buf[i] = byte(counter)
		counter >>= 8
	}
	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset]&0x7f) << 24) | (uint32(sum[offset+1]) << 16) | (uint32(sum[offset+2]) << 8) | uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < codeDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", codeDigits, code%mod)
}

// CodeAt computes the code a conforming authenticator app would show for
// secret at time at — the same computation ValidateAt checks against, just
// run forward instead of matched against a guess. Exported for tests and
// for any tool that needs to produce a code rather than check one (an
// authenticator app showing it is the normal production use; this package's
// own login/enrollment flow only ever calls ValidateAt).
func CodeAt(secret string, at time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return "", fmt.Errorf("invalid secret")
	}
	return hotp(key, uint64(at.Unix()/stepSecs)), nil
}

// ValidateAt checks code against secret at time at, allowing ±SkewSteps for
// clock drift. It returns the matched step number so a caller can reject a
// code already accepted once (replay protection): steps only increase with
// time, so "already used" is just "not greater than the last accepted step".
func ValidateAt(secret, code string, at time.Time) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != codeDigits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return 0, false
	}
	cur := at.Unix() / stepSecs
	for d := -SkewSteps; d <= SkewSteps; d++ {
		candidate := cur + int64(d)
		if candidate < 0 {
			continue
		}
		if hmac.Equal([]byte(hotp(key, uint64(candidate))), []byte(code)) {
			return candidate, true
		}
	}
	return 0, false
}
