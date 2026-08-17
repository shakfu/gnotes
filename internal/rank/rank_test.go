package rank

import (
	"errors"
	"math/big"
	"sort"
	"strings"
	"testing"
)

func TestMidIsInTheMiddle(t *testing.T) {
	m := Mid()
	if !Valid(m) {
		t.Fatalf("Mid() = %q is not a valid rank", m)
	}
	if m <= Min || m >= Max {
		t.Fatalf("Mid() = %q is not strictly between %q and %q", m, Min, Max)
	}
}

func TestBoundsAreValidAndOrdered(t *testing.T) {
	if !Valid(Min) || !Valid(Max) {
		t.Fatalf("bounds are not valid ranks: %q %q", Min, Max)
	}
	if Min >= Max {
		t.Fatalf("Min %q does not sort before Max %q", Min, Max)
	}
	if len(Min) != Width || len(Max) != Width {
		t.Fatalf("bounds are not %d chars: %d %d", Width, len(Min), len(Max))
	}
}

func TestBetweenProducesStrictlyOrderedRank(t *testing.T) {
	cases := []struct{ name, prev, next string }{
		{"open both ends", "", ""},
		{"prepend", "", Mid()},
		{"append", Mid(), ""},
		{"in the middle", Min, Max},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Between(c.prev, c.next)
			if err != nil {
				t.Fatalf("Between(%q, %q): %v", c.prev, c.next, err)
			}
			if !Valid(got) {
				t.Fatalf("result %q is not a valid rank", got)
			}
			if c.prev != "" && got <= c.prev {
				t.Fatalf("result %q does not sort after prev %q", got, c.prev)
			}
			if c.next != "" && got >= c.next {
				t.Fatalf("result %q does not sort before next %q", got, c.next)
			}
		})
	}
}

// Lexicographic comparison must agree with numeric comparison, which is the
// entire reason ranks are fixed width.
func TestLexicographicOrderMatchesNumericOrder(t *testing.T) {
	values := []int64{0, 1, 15, 16, 255, 256, 4095, 1 << 20}

	var ranks []string
	for _, v := range values {
		ranks = append(ranks, format(big.NewInt(v)))
	}

	shuffled := []string{ranks[4], ranks[0], ranks[7], ranks[2], ranks[5], ranks[1], ranks[6], ranks[3]}
	sort.Strings(shuffled)

	for i, want := range ranks {
		if shuffled[i] != want {
			t.Fatalf("at %d: string sort gave %q, numeric order wants %q", i, shuffled[i], want)
		}
	}
}

// Repeatedly inserting at the same spot must keep the list correctly ordered
// until the space between that pair runs out, and must then say so rather than
// return a duplicate or out-of-order rank.
func TestRepeatedInsertionHalvesUntilExhausted(t *testing.T) {
	lo, hi := Min, Max
	inserts := 0

	for {
		mid, err := Between(lo, hi)
		if errors.Is(err, ErrExhausted) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error after %d inserts: %v", inserts, err)
		}
		if mid <= lo || mid >= hi {
			t.Fatalf("insert %d produced %q outside (%q, %q)", inserts, mid, lo, hi)
		}
		hi = mid
		inserts++
		if inserts > Bits+8 {
			t.Fatalf("space did not exhaust after %d inserts", inserts)
		}
	}

	// Bisecting a 96-bit space reaches adjacent integers in about 96 steps.
	if inserts < Bits-1 || inserts > Bits {
		t.Fatalf("exhausted after %d inserts, expected about %d", inserts, Bits)
	}
}

func TestBetweenAdjacentValuesIsExhausted(t *testing.T) {
	lo := format(big.NewInt(100))
	hi := format(big.NewInt(101))

	if _, err := Between(lo, hi); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Between adjacent = %v, want ErrExhausted", err)
	}
}

// A stale view can present neighbours in the wrong order. That must route the
// caller through a rebalance rather than produce a rank that corrupts the list.
func TestBetweenInvertedNeighboursIsExhausted(t *testing.T) {
	if _, err := Between(Max, Min); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Between(Max, Min) = %v, want ErrExhausted", err)
	}
	if _, err := Between(Mid(), Mid()); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Between equal neighbours = %v, want ErrExhausted", err)
	}
}

func TestBetweenRejectsMalformedInput(t *testing.T) {
	bad := []string{"xyz", strings.Repeat("0", Width-1), strings.Repeat("F", Width), "0x" + strings.Repeat("0", Width-2)}

	for _, b := range bad {
		if _, err := Between(b, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("Between(%q, \"\") = %v, want ErrInvalid", b, err)
		}
		if _, err := Between("", b); !errors.Is(err, ErrInvalid) {
			t.Errorf("Between(\"\", %q) = %v, want ErrInvalid", b, err)
		}
	}
}

func TestValidRejectsUppercase(t *testing.T) {
	// Uppercase hex sorts before lowercase, so admitting it would silently
	// break the ordering of any list that mixed the two.
	if Valid(strings.ToUpper(Mid())) {
		t.Fatal("Valid accepted uppercase hex")
	}
}

func TestSpacedIsAscendingAndInBounds(t *testing.T) {
	for _, n := range []int{1, 2, 3, 10, 500} {
		got, err := Spaced(n)
		if err != nil {
			t.Fatalf("Spaced(%d): %v", n, err)
		}
		if len(got) != n {
			t.Fatalf("Spaced(%d) returned %d ranks", n, len(got))
		}
		for i, r := range got {
			if !Valid(r) {
				t.Fatalf("Spaced(%d)[%d] = %q is invalid", n, i, r)
			}
			if r <= Min || r >= Max {
				t.Fatalf("Spaced(%d)[%d] = %q is not strictly inside the space", n, i, r)
			}
			if i > 0 && got[i-1] >= r {
				t.Fatalf("Spaced(%d) not ascending at %d: %q >= %q", n, i, got[i-1], r)
			}
		}
	}
}

// After a rebalance there must be room to insert between every adjacent pair,
// otherwise the rebalance would not have unblocked the operation that asked
// for it.
func TestSpacedLeavesRoomBetweenEveryPair(t *testing.T) {
	got, err := Spaced(64)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(got); i++ {
		if _, err := Between(got[i-1], got[i]); err != nil {
			t.Fatalf("no room between rebalanced ranks %d and %d: %v", i-1, i, err)
		}
	}
	// And room at both ends.
	if _, err := Between("", got[0]); err != nil {
		t.Fatalf("no room before the first rebalanced rank: %v", err)
	}
	if _, err := Between(got[len(got)-1], ""); err != nil {
		t.Fatalf("no room after the last rebalanced rank: %v", err)
	}
}

func TestSpacedEdgeCases(t *testing.T) {
	if got, err := Spaced(0); err != nil || got != nil {
		t.Fatalf("Spaced(0) = %v, %v; want nil, nil", got, err)
	}
	if _, err := Spaced(-1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Spaced(-1) = %v, want ErrInvalid", err)
	}
}

// buildList applies a sequence of placements and returns the resulting order,
// mirroring how the materializer maintains a parent's children.
func buildList(t *testing.T, ops []struct {
	id  string
	pos Position
}) []Sibling {
	t.Helper()
	var list []Sibling

	for _, op := range ops {
		r, err := Resolve(list, op.pos)
		if err != nil {
			t.Fatalf("Resolve for %s: %v", op.id, err)
		}
		list = append(list, Sibling{ID: op.id, Rank: r})
		sort.Slice(list, func(i, j int) bool { return list[i].Rank < list[j].Rank })
	}
	return list
}

func TestResolvePlacesNodesInTheRequestedOrder(t *testing.T) {
	list := buildList(t, []struct {
		id  string
		pos Position
	}{
		{"a", End()},
		{"b", End()},
		{"c", Start()},
		{"d", After("a")},
		{"e", Before("b")},
	})

	var ids []string
	for _, s := range list {
		ids = append(ids, s.ID)
	}

	want := []string{"c", "a", "d", "e", "b"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", ids, want)
	}
}

func TestResolveOnEmptyListReturnsMid(t *testing.T) {
	got, err := Resolve(nil, End())
	if err != nil {
		t.Fatal(err)
	}
	if got != Mid() {
		t.Fatalf("Resolve on empty list = %q, want Mid() %q", got, Mid())
	}
}

// A Before/After reference goes stale when another machine moved or deleted
// that sibling first. Appending is a deliberate fallback; dropping the
// operation would be worse.
func TestResolveAppendsWhenSiblingReferenceIsStale(t *testing.T) {
	list := []Sibling{{ID: "a", Rank: Mid()}}

	for _, pos := range []Position{Before("gone"), After("gone")} {
		got, err := Resolve(list, pos)
		if err != nil {
			t.Fatalf("Resolve(%v): %v", pos, err)
		}
		if got <= list[0].Rank {
			t.Fatalf("Resolve(%v) = %q, want a rank after %q", pos, got, list[0].Rank)
		}
	}
}

func TestResolveRejectsUnknownPosition(t *testing.T) {
	list := []Sibling{{ID: "a", Rank: Mid()}}
	if _, err := Resolve(list, Position{At: "sideways"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Resolve with unknown position = %v, want ErrInvalid", err)
	}
}

// The empty At is the zero Position and must behave as End, so a caller that
// omits the field appends rather than erroring.
func TestResolveZeroPositionAppends(t *testing.T) {
	list := []Sibling{{ID: "a", Rank: Mid()}}
	got, err := Resolve(list, Position{})
	if err != nil {
		t.Fatal(err)
	}
	if got <= list[0].Rank {
		t.Fatalf("zero Position gave %q, want a rank after %q", got, list[0].Rank)
	}
}

// Prepending repeatedly is the pattern most likely to exhaust the space in
// real use (a user adding items to the top of a list), so it must stay ordered
// for a realistic number of operations without any rebalance.
func TestManyPrependsStayOrdered(t *testing.T) {
	var list []Sibling
	for i := 0; i < 64; i++ {
		r, err := Resolve(list, Start())
		if err != nil {
			t.Fatalf("prepend %d: %v", i, err)
		}
		list = append([]Sibling{{ID: string(rune('a' + i%26)), Rank: r}}, list...)
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Rank >= list[i].Rank {
			t.Fatalf("list not ascending at %d", i)
		}
	}
}

func BenchmarkBetween(b *testing.B) {
	lo, hi := Min, Max
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Between(lo, hi)
	}
}

func BenchmarkResolveAppend(b *testing.B) {
	list := make([]Sibling, 0, 1000)
	ranks, _ := Spaced(1000)
	for i, r := range ranks {
		list = append(list, Sibling{ID: string(rune(i)), Rank: r})
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Resolve(list, End())
	}
}
