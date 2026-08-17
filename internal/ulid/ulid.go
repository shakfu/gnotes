// Package ulid implements the subset of ULID needed by gnotes: generation
// with monotonic ordering inside a millisecond, and timestamp extraction.
//
// A ULID is 128 bits (48-bit big-endian millisecond timestamp, 80 bits of
// entropy) rendered as 26 characters of Crockford base32. The encoding is
// order-preserving, so lexicographic comparison of two ULID strings matches
// chronological comparison of the instants they encode. gnotes leans on that
// everywhere it needs a deterministic tiebreak between concurrent events.
package ulid

import (
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"time"
)

// Len is the length of an encoded ULID in characters.
const Len = 26

// maxTime is the largest instant representable in 48 bits of milliseconds.
const maxTime = uint64(1)<<48 - 1

// encoding is Crockford base32: the digits, then the uppercase alphabet with
// I, L, O and U removed so that visually ambiguous characters cannot occur.
const encoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// dec maps a byte back to its base32 value, or 0xFF when the byte is not a
// legal Crockford character. Built once at init so decoding is a table lookup.
var dec [256]byte

func init() {
	for i := range dec {
		dec[i] = 0xFF
	}
	for i := 0; i < len(encoding); i++ {
		dec[encoding[i]] = byte(i)
		// Accept lowercase on input. Filenames get lowercased on some
		// filesystems, and a ULID that survives that round trip is worth more
		// than strictness we would only have to work around later.
		dec[encoding[i]|0x20] = byte(i)
	}
}

// ErrInvalid reports a string that is not a well-formed ULID.
var ErrInvalid = errors.New("ulid: invalid")

// ID is an encoded ULID. It is a string rather than a [16]byte because every
// consumer in gnotes (JSON payloads, map keys, sort keys, filenames) wants the
// text form, and the binary form would only be converted back immediately.
type ID = string

// Generator produces monotonically increasing ULIDs. The zero value is not
// usable; call NewGenerator.
//
// Monotonicity matters because gnotes appends several events in a single
// operation (create a node, then rank it) and replay must see them in the
// order they were written. Within a millisecond the generator increments the
// entropy of the previous ID instead of drawing fresh bytes, which keeps the
// sequence strictly ascending.
type Generator struct {
	mu       sync.Mutex
	lastTime uint64
	lastRand [10]byte
	now      func() time.Time
}

// NewGenerator returns a Generator that reads the wall clock.
func NewGenerator() *Generator {
	return NewGeneratorAt(time.Now)
}

// NewGeneratorAt returns a Generator driven by the supplied clock rather than
// the wall clock.
//
// An event's id is also its timestamp, so anything that wants to control when
// events appear to have happened has to control this, not just the clock used
// for parsing dates.
func NewGeneratorAt(now func() time.Time) *Generator {
	return &Generator{now: now}
}

// New returns the next ULID, no earlier than the current wall clock.
func (g *Generator) New() ID {
	return g.NewAfter(0)
}

// NewAfter returns the next ULID whose timestamp is at least floorMS. The
// caller passes the timestamp of the last event it observed so that a machine
// with a lagging clock still appends IDs that sort after what is already in
// the log. Without this, a clock skew of a few seconds between two users would
// interleave their events in the wrong order forever.
func (g *Generator) NewAfter(floorMS uint64) ID {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := uint64(g.now().UnixMilli())
	if floorMS > ms {
		ms = floorMS
	}
	if ms > maxTime {
		ms = maxTime
	}

	if ms > g.lastTime {
		g.lastTime = ms
		randBytes(g.lastRand[:])
	} else {
		// Same millisecond (or the clock moved backwards): keep the previous
		// timestamp and increment the entropy so the new ID still sorts after
		// the old one.
		ms = g.lastTime
		if !increment(g.lastRand[:]) {
			// 2^80 IDs in one millisecond is not reachable in practice, but
			// wrapping would break ordering, so step the clock instead.
			g.lastTime++
			ms = g.lastTime
			randBytes(g.lastRand[:])
		}
	}

	return encode(ms, g.lastRand)
}

// increment adds one to a big-endian byte slice, reporting false on overflow.
func increment(b []byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return true
		}
	}
	return false
}

// randBytes fills b with cryptographic randomness. crypto/rand.Read does not
// fail on any platform gnotes targets; a panic here is preferable to silently
// emitting predictable IDs that could collide across machines.
func randBytes(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic("ulid: entropy source failed: " + err.Error())
	}
}

// encode renders a timestamp and entropy as 26 Crockford base32 characters.
//
// The 128 bits do not divide evenly into 5-bit groups (26*5 = 130), so the
// first character carries only the top 2 bits and the layout is written out
// explicitly rather than looped. Timestamp and entropy are encoded separately
// because the 48-bit boundary falls on character 10, exactly on a group edge.
func encode(ms uint64, entropy [10]byte) string {
	var b [Len]byte

	// 48 bits of time across the first 10 characters.
	b[0] = encoding[(ms>>45)&0x1F]
	b[1] = encoding[(ms>>40)&0x1F]
	b[2] = encoding[(ms>>35)&0x1F]
	b[3] = encoding[(ms>>30)&0x1F]
	b[4] = encoding[(ms>>25)&0x1F]
	b[5] = encoding[(ms>>20)&0x1F]
	b[6] = encoding[(ms>>15)&0x1F]
	b[7] = encoding[(ms>>10)&0x1F]
	b[8] = encoding[(ms>>5)&0x1F]
	b[9] = encoding[ms&0x1F]

	// 80 bits of entropy across the remaining 16 characters.
	e := entropy
	b[10] = encoding[e[0]>>3]
	b[11] = encoding[(e[0]&0x07)<<2|e[1]>>6]
	b[12] = encoding[(e[1]>>1)&0x1F]
	b[13] = encoding[(e[1]&0x01)<<4|e[2]>>4]
	b[14] = encoding[(e[2]&0x0F)<<1|e[3]>>7]
	b[15] = encoding[(e[3]>>2)&0x1F]
	b[16] = encoding[(e[3]&0x03)<<3|e[4]>>5]
	b[17] = encoding[e[4]&0x1F]
	b[18] = encoding[e[5]>>3]
	b[19] = encoding[(e[5]&0x07)<<2|e[6]>>6]
	b[20] = encoding[(e[6]>>1)&0x1F]
	b[21] = encoding[(e[6]&0x01)<<4|e[7]>>4]
	b[22] = encoding[(e[7]&0x0F)<<1|e[8]>>7]
	b[23] = encoding[(e[8]>>2)&0x1F]
	b[24] = encoding[(e[8]&0x03)<<3|e[9]>>5]
	b[25] = encoding[e[9]&0x1F]

	return string(b[:])
}

// Valid reports whether s is a syntactically well-formed ULID.
func Valid(s string) bool {
	if len(s) != Len {
		return false
	}
	// The first character encodes the top 2 bits of the timestamp, so anything
	// above '7' would overflow 48 bits and is not a legal ULID.
	if dec[s[0]] > 7 {
		return false
	}
	for i := 0; i < Len; i++ {
		if dec[s[i]] == 0xFF {
			return false
		}
	}
	return true
}

// Time returns the millisecond timestamp encoded in s.
func Time(s string) (uint64, error) {
	if !Valid(s) {
		return 0, ErrInvalid
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		ms = ms<<5 | uint64(dec[s[i]])
	}
	return ms, nil
}

// Timestamp returns the instant encoded in s.
func Timestamp(s string) (time.Time, error) {
	ms, err := Time(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}

// Canonical uppercases a ULID that survived a case-folding filesystem. Input
// that is not a ULID is returned unchanged rather than mangled, so a caller
// that guessed wrong about the string still gets its original back.
func Canonical(s string) string {
	if len(s) != Len {
		return s
	}
	for i := 0; i < Len; i++ {
		if dec[s[i]] == 0xFF {
			return s
		}
	}
	return strings.ToUpper(s)
}

// Short returns the trailing n characters of a ULID, the human-facing handle
// used to refer to a node on the command line. The tail is used rather than
// the head because the leading characters are the timestamp and are identical
// across everything created in the same period.
func Short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
