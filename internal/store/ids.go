package store

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewID returns a UUIDv7 (time-ordered) as canonical text.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())                                                                               // #nosec G115 -- milliseconds since 1970 fit
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms) // #nosec G115 -- byte extraction of the 48-bit timestamp is the UUIDv7 layout
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
