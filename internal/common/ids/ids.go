// Package ids generates UUIDv7 identifiers (time ordered, random tail) and secrets.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

var (
	mu       sync.Mutex
	lastMS   int64
	sequence uint16
)

// New returns a new UUIDv7 string. IDs created by one process are strictly increasing.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	mu.Lock()
	ms := time.Now().UnixMilli()
	if ms <= lastMS {
		ms = lastMS
		sequence++
		if sequence > 0x0fff {
			ms++
			sequence = 0
		}
	} else {
		sequence = uint16(b[6]&0x03)<<8 | uint16(b[7])
	}
	lastMS = ms
	seq := sequence
	mu.Unlock()
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (40 - 8*i))
	}
	b[6] = 0x70 | byte(seq>>8)&0x0f
	b[7] = byte(seq)
	b[8] = b[8]&0x3f | 0x80
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:], b[10:])
	return string(out[:])
}

// Valid reports whether s is a canonical lowercase UUID string.
func Valid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// Token returns n random bytes, hex encoded.
func Token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
