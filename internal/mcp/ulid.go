package mcp

import (
	"crypto/rand"
	"strings"
	"time"
)

// crockfordAlphabet is the Base32 alphabet used by the ULID spec
// (excludes I, L, O, U to avoid visual ambiguity).
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// genULID generates a real ULID: 48-bit millisecond timestamp (10 chars,
// lexicographically sortable) + 80-bit crypto-random entropy (16 chars),
// Crockford Base32 encoded per https://github.com/ulid/spec.
// This makes remember-generated IDs chronologically orderable, unlike a
// bare random hex string.
func genULID() string {
	ms := uint64(time.Now().UnixMilli())
	var entropy [10]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		// crypto/rand failure is exceptionally rare (OS entropy source
		// down); fall back to zero entropy rather than panicking — the
		// timestamp component alone still gives uniqueness at ms granularity
		// for the overwhelmingly common single-writer case.
		entropy = [10]byte{} // explicitly zero — documents intent, silences SA9003
	}

	var sb strings.Builder
	sb.Grow(26)

	// Timestamp: 48 bits -> 10 Base32 chars (5 bits each)
	for i := 9; i >= 0; i-- {
		shift := uint(i * 5)
		idx := (ms >> shift) & 0x1F
		sb.WriteByte(crockfordAlphabet[idx])
	}

	// Entropy: 80 bits -> 16 Base32 chars, encoded from the 10 random bytes
	var bits uint64
	var bitCount uint
	byteIdx := 0
	for sb.Len() < 26 {
		for bitCount < 5 && byteIdx < len(entropy) {
			bits = (bits << 8) | uint64(entropy[byteIdx])
			bitCount += 8
			byteIdx++
		}
		if bitCount < 5 {
			bits <<= (5 - bitCount)
			bitCount = 5
		}
		shift := bitCount - 5
		idx := (bits >> shift) & 0x1F
		sb.WriteByte(crockfordAlphabet[idx])
		bitCount -= 5
	}

	return sb.String()
}
