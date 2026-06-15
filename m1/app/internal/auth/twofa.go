// internal/auth/twofa.go
//
// TOTP 2FA, dependency-free (RFC 6238 / RFC 4226, HMAC-SHA1, 30s step, 6 digits)
// plus single-use recovery codes. No external module so the build never depends
// on fetching a package over the Whonix/Tor network.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	totpDigits   = 6
	totpPeriod   = 30 // seconds
	totpSkew     = 1  // accept the previous/next window (clock drift)
	totpIssuer   = "grams"
	pendingTTL   = 5 * time.Minute
	recoveryCnt  = 10
	recoveryLen  = 10 // chars per code (base32, no padding)
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns a fresh base32 secret (160-bit, standard for authenticators).
func newTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

// otpauthURI builds the otpauth:// URL an authenticator app imports (QR target).
func otpauthURI(secret, account string) string {
	label := url.PathEscape(totpIssuer + ":" + account)
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", totpIssuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprintf("%d", totpDigits))
	v.Set("period", fmt.Sprintf("%d", totpPeriod))
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// totpAt computes the code for a given secret + unix time.
func totpAt(secret string, t time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	counter := uint64(t.Unix()) / totpPeriod
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off]&0x7f) << 24) |
		(uint32(sum[off+1]) << 16) |
		(uint32(sum[off+2]) << 8) |
		uint32(sum[off+3])
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, bin%mod), nil
}

// verifyTOTP checks a user-supplied code against the secret, allowing +/- skew
// windows for clock drift. Constant-time compare on the digits.
func verifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	now := time.Now()
	for w := -totpSkew; w <= totpSkew; w++ {
		want, err := totpAt(secret, now.Add(time.Duration(w*totpPeriod)*time.Second))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// genRecoveryCodes returns N human-readable single-use codes (plaintext, shown
// once) and their bcrypt hashes (stored). Format: xxxxx-xxxxx (base32, no 0/1/O/I confusion).
func genRecoveryCodes() (plain []string, hashed []string, err error) {
	const alpha = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/1/O/I
	for i := 0; i < recoveryCnt; i++ {
		raw := make([]byte, recoveryLen)
		if _, err = rand.Read(raw); err != nil {
			return nil, nil, err
		}
		var sb strings.Builder
		for j, b := range raw {
			if j == recoveryLen/2 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alpha[int(b)%len(alpha)])
		}
		code := sb.String()
		h, herr := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
		if herr != nil {
			return nil, nil, herr
		}
		plain = append(plain, code)
		hashed = append(hashed, string(h))
	}
	return plain, hashed, nil
}

// useRecoveryCode checks a supplied code against the user's stored hashes.
// On match it removes that hash (single-use) and returns true.
func (a *Auth) useRecoveryCode(ctx context.Context, uid int64, code string) bool {
	code = strings.TrimSpace(strings.ToUpper(code))
	var hashes []string
	if err := a.pool.QueryRow(ctx,
		`SELECT recovery_codes FROM users WHERE id=$1`, uid).Scan(&hashes); err != nil {
		return false
	}
	for i, h := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(code)) == nil {
			// remove index i (single-use), write back
			remaining := append(append([]string{}, hashes[:i]...), hashes[i+1:]...)
			_, _ = a.pool.Exec(ctx,
				`UPDATE users SET recovery_codes=$2 WHERE id=$1`, uid, remaining)
			return true
		}
	}
	return false
}
