// Package rank implements the fractional ordering keys that place siblings
// under a parent node.
//
// A rank is a fixed-width lowercase hex string encoding a 96-bit unsigned
// integer. Fixed width is the whole trick: it makes lexicographic string
// comparison identical to numeric comparison, so ordering siblings never
// requires parsing a rank, only comparing two strings. Arithmetic happens
// solely at insertion time.
//
// Inserting between two neighbours takes their midpoint. Repeatedly inserting
// at the same spot halves the gap each time, so after at most 96 inserts the
// space between a specific pair is exhausted. That is detected rather than
// silently mishandled, and the caller responds by emitting a rebalance event
// that respaces every child of the parent.
package rank

import (
	"errors"
	"math/big"
	"strings"
)

// Width is the number of hex characters in a rank: 24 chars * 4 bits = 96 bits.
//
// 96 bits is chosen over 64 because the depth of the space is what bounds how
// often a rebalance is needed, and a rebalance rewrites every sibling. It is
// chosen over 128 because 24 characters still fits comfortably in a log line
// and in a debugger's field of view.
const Width = 24

// Bits is the width of the rank space in bits.
const Bits = Width * 4

// ErrExhausted reports that no rank exists strictly between two neighbours.
// The caller is expected to rebalance the parent's children and retry, not to
// treat this as a failure of the operation.
var ErrExhausted = errors.New("rank: no space between neighbours")

// ErrInvalid reports a malformed rank string.
var ErrInvalid = errors.New("rank: invalid")

// maxRank is the largest value the space can hold, 2^96 - 1.
var maxRank = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), Bits), big.NewInt(1))

// Min and Max are the bounds of the rank space. They are never assigned to a
// node; they exist so that an insert at the very start or very end has a
// neighbour to take a midpoint against.
var (
	Min = strings.Repeat("0", Width)
	Max = format(maxRank)
)

// Valid reports whether s is a well-formed rank: exactly Width lowercase hex
// characters. Uppercase is rejected because it would sort before lowercase and
// silently corrupt the ordering of a list that mixed the two.
func Valid(s string) bool {
	if len(s) != Width {
		return false
	}
	for i := 0; i < Width; i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// parse converts a rank to its integer value.
func parse(s string) (*big.Int, error) {
	if !Valid(s) {
		return nil, ErrInvalid
	}
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return nil, ErrInvalid
	}
	return n, nil
}

// format renders an integer as a zero-padded fixed-width hex rank.
func format(n *big.Int) string {
	h := n.Text(16)
	if len(h) >= Width {
		return h
	}
	var b strings.Builder
	b.Grow(Width)
	for i := len(h); i < Width; i++ {
		b.WriteByte('0')
	}
	b.WriteString(h)
	return b.String()
}

// Mid returns the rank at the centre of the space, the rank given to the first
// child of a parent. Starting in the middle rather than at either end leaves
// maximal room to prepend and append without an immediate rebalance.
func Mid() string {
	return format(new(big.Int).Rsh(maxRank, 1))
}

// Between returns a rank strictly between prev and next.
//
// An empty prev means "before the first sibling" and an empty next means
// "after the last sibling"; passing both empty asks for the rank of a list's
// only child. It returns ErrExhausted when the two neighbours are adjacent
// integers, leaving no value in between.
func Between(prev, next string) (string, error) {
	lo := new(big.Int)
	if prev != "" {
		n, err := parse(prev)
		if err != nil {
			return "", err
		}
		lo = n
	}

	hi := new(big.Int).Set(maxRank)
	if next != "" {
		n, err := parse(next)
		if err != nil {
			return "", err
		}
		hi = n
	}

	// Neighbours out of order means the caller's view of the list disagrees
	// with the stored ranks, which happens when a concurrent move landed
	// between the read and the write. Exhaustion is the honest answer: it
	// sends the caller through a rebalance, after which its view is correct.
	if hi.Cmp(lo) <= 0 {
		return "", ErrExhausted
	}

	mid := new(big.Int).Add(lo, hi)
	mid.Rsh(mid, 1)

	if mid.Cmp(lo) == 0 || mid.Cmp(hi) == 0 {
		return "", ErrExhausted
	}
	return format(mid), nil
}

// Spaced returns count ranks evenly distributed across the whole space. It
// backs the rebalance event: respacing every child of a parent restores the
// maximum possible gap between each adjacent pair.
func Spaced(count int) ([]string, error) {
	if count < 0 {
		return nil, ErrInvalid
	}
	if count == 0 {
		return nil, nil
	}
	// Beyond 2^96 children the space cannot hold one rank each. Unreachable in
	// practice, but the arithmetic below would produce duplicates rather than
	// an error, and duplicate ranks make sibling order non-deterministic.
	if big.NewInt(int64(count)).Cmp(maxRank) >= 0 {
		return nil, ErrExhausted
	}

	out := make([]string, count)
	step := new(big.Int)
	divisor := big.NewInt(int64(count) + 1)
	idx := new(big.Int)

	for i := 0; i < count; i++ {
		// rank_i = maxRank * (i+1) / (count+1), keeping both endpoints free so
		// there is always room to prepend and append after a rebalance.
		idx.SetInt64(int64(i + 1))
		step.Mul(maxRank, idx)
		step.Div(step, divisor)
		out[i] = format(step)
	}
	return out, nil
}

// Position describes where a node should land among its siblings.
type Position struct {
	// At is one of PosStart, PosEnd, PosBefore or PosAfter.
	At string
	// Sibling is the node id that PosBefore and PosAfter are relative to.
	Sibling string
}

// Placement constants for Position.At.
const (
	PosStart  = "start"
	PosEnd    = "end"
	PosBefore = "before"
	PosAfter  = "after"
)

// Start returns a Position placing a node first among its siblings.
func Start() Position { return Position{At: PosStart} }

// End returns a Position placing a node last among its siblings.
func End() Position { return Position{At: PosEnd} }

// Before returns a Position placing a node immediately before sibling.
func Before(sibling string) Position { return Position{At: PosBefore, Sibling: sibling} }

// After returns a Position placing a node immediately after sibling.
func After(sibling string) Position { return Position{At: PosAfter, Sibling: sibling} }

// Sibling is the minimum a caller must know about an existing child for Resolve
// to place a new node relative to it.
type Sibling struct {
	ID   string
	Rank string
}

// Resolve computes the rank for a node placed at pos among siblings, which must
// already be sorted ascending by rank.
//
// When Before or After names a sibling that is not in the list, the node is
// appended instead. That reference is an ordering hint from whatever the caller
// last rendered, and it goes stale whenever another machine moved or deleted
// the node first. Landing at the end is a worse answer than the caller asked
// for, but refusing outright would drop the operation entirely.
func Resolve(siblings []Sibling, pos Position) (string, error) {
	if len(siblings) == 0 {
		return Mid(), nil
	}

	first := siblings[0].Rank
	last := siblings[len(siblings)-1].Rank

	switch pos.At {
	case PosStart:
		return Between("", first)

	case PosEnd, "":
		return Between(last, "")

	case PosBefore:
		i := indexOf(siblings, pos.Sibling)
		if i < 0 {
			return Between(last, "")
		}
		prev := ""
		if i > 0 {
			prev = siblings[i-1].Rank
		}
		return Between(prev, siblings[i].Rank)

	case PosAfter:
		i := indexOf(siblings, pos.Sibling)
		if i < 0 {
			return Between(last, "")
		}
		next := ""
		if i < len(siblings)-1 {
			next = siblings[i+1].Rank
		}
		return Between(siblings[i].Rank, next)

	default:
		return "", ErrInvalid
	}
}

// indexOf returns the position of id in siblings, or -1.
func indexOf(siblings []Sibling, id string) int {
	for i := range siblings {
		if siblings[i].ID == id {
			return i
		}
	}
	return -1
}
