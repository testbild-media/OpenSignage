// Package passwords hashes and verifies passwords with Argon2id (PHC string format).
package passwords

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params of Argon2id. Server and players use the same moderate setting so a Raspberry
// Pi 3 can verify a login in well under a second.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// Default parameters (OWASP minimum recommendation for Argon2id: 19 MiB, t=2, p=1).
var Default = Params{Memory: 19 * 1024, Time: 2, Threads: 1}

// sem limits concurrent hashing so login floods cannot exhaust memory.
var sem = make(chan struct{}, 2)

// MinLength is the minimum password length.
const MinLength = 8

// ErrTooShort / ErrTooLong are policy errors.
var (
	ErrTooShort = errors.New("password_too_short")
	ErrTooLong  = errors.New("password_too_long")
)

// CheckPolicy validates a new password.
func CheckPolicy(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinLength {
		return ErrTooShort
	}
	if len(pw) > 256 {
		return ErrTooLong
	}
	return nil
}

// Hash returns a PHC-formatted Argon2id hash.
func Hash(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := Default
	sem <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, 32)
	<-sem
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify checks pw against a PHC hash in constant time.
func Verify(pw, phc string) bool {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false
	}
	if p.Memory > 256*1024 || p.Time > 10 || p.Threads == 0 {
		return false // refuse absurd parameters (DoS)
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	sem <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	<-sem
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyVerify spends the same time as a real verification (prevents user enumeration).
func DummyVerify() {
	Verify("dummy-password", dummyHash)
}

var dummyHash = func() string {
	h, err := Hash("not-a-real-password")
	if err != nil {
		panic(err)
	}
	return h
}()
