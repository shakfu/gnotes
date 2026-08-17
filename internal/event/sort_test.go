package event

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// mkID builds a real ULID carrying the given millisecond timestamp, with
// suffix distinguishing otherwise identical ids. Tests use it to state an
// expected merge order directly rather than generating ids and discovering it.
//
// The timestamp is encoded as genuine Crockford base32 so that ulid.Time
// decodes it back to ms; writing the digits out in decimal would produce an id
// that sorts as intended but dates to something else entirely.
func mkID(ms int, suffix string) string {
	const enc = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	var b [26]byte
	v := uint64(ms)
	for i := 9; i >= 0; i-- {
		b[i] = enc[v&0x1F]
		v >>= 5
	}
	for i := 10; i < 26; i++ {
		b[i] = '0'
	}
	// The suffix occupies the high end of the entropy, so it dominates the
	// tiebreak between two ids sharing a timestamp.
	copy(b[10:], suffix)
	return string(b[:])
}

// chain builds a linear run of events, each referring to the one before it,
// which is what a single author writing offline produces.
func chain(user string, root string, ms int, n int) []Event {
	out := make([]Event, 0, n)
	ref := root
	for i := 0; i < n; i++ {
		id := mkID(ms+i, user)
		out = append(out, Event{ID: id, Ref: ref, Action: AddNote, UserID: user})
		ref = id
	}
	return out
}

func ids(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

func TestSortEmptyAndSingle(t *testing.T) {
	if got := Sort(nil); len(got) != 0 {
		t.Fatalf("Sort(nil) = %v", got)
	}
	one := []Event{{ID: mkID(1, "A")}}
	if got := Sort(one); len(got) != 1 || got[0].ID != one[0].ID {
		t.Fatalf("Sort of one event = %v", ids(got))
	}
}

// Sort is documented to reorder in place; Sorted is the copying variant for
// callers that do not own their slice.
func TestSortIsInPlaceAndSortedIsNot(t *testing.T) {
	// Ids descend while refs ascend, so canonical order differs from input
	// order and an in-place sort is observable.
	a := Event{ID: mkID(300, "A")}
	b := Event{ID: mkID(200, "A"), Ref: a.ID}
	in := []Event{b, a}

	out := Sorted(in)
	if in[0].ID != b.ID {
		t.Fatal("Sorted mutated its input")
	}
	if out[0].ID != a.ID {
		t.Fatalf("Sorted returned the wrong order: %v", ids(out))
	}

	got := Sort(in)
	if in[0].ID != a.ID {
		t.Fatal("Sort did not reorder in place")
	}
	if &got[0] != &in[0] {
		t.Fatal("Sort returned a different slice than it was given")
	}
}

func TestSortFollowsRefChain(t *testing.T) {
	// Ids descend while refs ascend, so only the ref chain can produce the
	// right answer; sorting by id alone would reverse it.
	a := Event{ID: mkID(300, "A"), Ref: ""}
	b := Event{ID: mkID(200, "A"), Ref: a.ID}
	c := Event{ID: mkID(100, "A"), Ref: b.ID}

	got := ids(Sort([]Event{c, a, b}))
	want := []string{a.ID, b.ID, c.ID}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// The property the whole design rests on: the same events in any arrival order
// must produce one identical sequence.
func TestSortConvergesFromAnyInputOrder(t *testing.T) {
	root := chain("R", "", 1, 2)
	// Two authors branch from the same point and work offline.
	left := chain("A", root[len(root)-1].ID, 10, 4)
	right := chain("B", root[len(root)-1].ID, 20, 4)

	all := append(append(append([]Event{}, root...), left...), right...)
	want := strings.Join(ids(Sorted(all)), ",")

	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 200; trial++ {
		shuffled := append([]Event(nil), all...)
		rng.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})

		if got := strings.Join(ids(Sorted(shuffled)), ","); got != want {
			t.Fatalf("trial %d diverged:\n got %s\nwant %s", trial, got, want)
		}
	}
}

// Each author's run must stay contiguous. Interleaving two authors' events
// would replay each one's edits out of the order they made them.
func TestSortKeepsEachAuthorsRunContiguous(t *testing.T) {
	root := chain("R", "", 1, 1)
	left := chain("A", root[0].ID, 10, 3)
	right := chain("B", root[0].ID, 20, 3)

	got := Sort(append(append(append([]Event{}, root...), right...), left...))

	var seq []string
	for _, e := range got {
		if len(seq) == 0 || seq[len(seq)-1] != e.UserID {
			seq = append(seq, e.UserID)
		}
	}
	// R once, then each author exactly once: three runs, no author twice.
	if len(seq) != 3 {
		t.Fatalf("author runs = %v, want three contiguous runs", seq)
	}
}

// Two authors branching from the same event are ordered by ULID, which is
// stable across machines in a way that wall-clock comparison is not.
func TestSortBreaksTiesByULID(t *testing.T) {
	root := Event{ID: mkID(1, "R")}
	// Deliberately give the later-listed branch the lower id.
	high := Event{ID: mkID(900, "A"), Ref: root.ID}
	low := Event{ID: mkID(100, "B"), Ref: root.ID}

	got := ids(Sort([]Event{root, high, low}))
	want := []string{root.ID, low.ID, high.ID}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// Pulling one author's log before the log it branched from must not drop
// their events, and must not depend on which partial set arrived.
func TestSortKeepsOrphansWhoseAncestorIsMissing(t *testing.T) {
	orphans := chain("A", mkID(1, "MZZZZZ"), 10, 3)
	present := chain("B", "", 20, 2)

	got := Sort(append(append([]Event{}, present...), orphans...))

	if len(got) != 5 {
		t.Fatalf("got %d events, want all 5 kept: %v", len(got), ids(got))
	}
	// The real root comes first; the orphan run is appended after it, intact.
	if got[0].ID != present[0].ID {
		t.Fatalf("rooted events did not come first: %v", ids(got))
	}
	for i, want := range orphans {
		if got[2+i].ID != want.ID {
			t.Fatalf("orphan run broken at %d: %v", i, ids(got))
		}
	}
}

// When the missing ancestor finally syncs in, the orphans must fold back into
// their proper place rather than staying stranded at the end.
func TestSortReattachesOrphansOnceAncestorArrives(t *testing.T) {
	ancestor := Event{ID: mkID(1, "R"), Action: AddNote}
	orphans := chain("A", ancestor.ID, 10, 3)

	without := Sorted(orphans)
	with := Sorted(append([]Event{ancestor}, orphans...))

	if without[0].ID != orphans[0].ID {
		t.Fatalf("without the ancestor, orphans should still replay in order: %v", ids(without))
	}
	if with[0].ID != ancestor.ID {
		t.Fatalf("with the ancestor present it must come first: %v", ids(with))
	}
	for i := range orphans {
		if with[1+i].ID != orphans[i].ID {
			t.Fatalf("orphans did not reattach in order: %v", ids(with))
		}
	}
}

// A ref cycle cannot be produced by a correct writer, but a corrupted or
// hand-edited log must degrade to a stable order rather than lose records.
func TestSortKeepsCyclicEventsInStableOrder(t *testing.T) {
	a := Event{ID: mkID(100, "A")}
	b := Event{ID: mkID(200, "B")}
	a.Ref, b.Ref = b.ID, a.ID

	first := ids(Sort([]Event{a, b}))
	second := ids(Sort([]Event{b, a}))

	if len(first) != 2 {
		t.Fatalf("cycle lost events: %v", first)
	}
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("cycle ordering unstable: %v vs %v", first, second)
	}
	if first[0] != a.ID {
		t.Fatalf("cycle should enter at the lowest ULID, got %v", first)
	}
}

// The normal shape of the ref tree is a chain as long as the log, so the walk
// must not nest once per event.
func TestSortHandlesVeryLongChain(t *testing.T) {
	const n = 200_000
	got := Sort(chain("A", "", 1, n))

	if len(got) != n {
		t.Fatalf("got %d events, want %d", len(got), n)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Ref != got[i-1].ID {
			t.Fatalf("chain broken at %d", i)
		}
	}
}

func TestEdgeRef(t *testing.T) {
	if got := EdgeRef(nil); got != "" {
		t.Fatalf("EdgeRef(nil) = %q, want empty", got)
	}
	c := chain("A", "", 1, 3)
	if got := EdgeRef(c); got != c[2].ID {
		t.Fatalf("EdgeRef = %q, want %q", got, c[2].ID)
	}
}

func TestSplitAtPartitionsByTime(t *testing.T) {
	c := chain("A", "", 100, 5) // timestamps 100..104

	before, after := SplitAt(c, 102)

	if len(before) != 3 || len(after) != 2 {
		t.Fatalf("split = %d before, %d after; want 3 and 2", len(before), len(after))
	}
	if before[2].ID != c[2].ID {
		t.Fatalf("cutoff is inclusive of the boundary event; got %v", ids(before))
	}
}

// An event must never be replayed without its causal ancestor, even when a
// skewed clock puts its own timestamp inside the window.
func TestSplitAtHoldsBackDescendantsOfExcludedEvents(t *testing.T) {
	early := Event{ID: mkID(100, "A")}
	// Written later in causal order but stamped earlier by a lagging clock.
	late := Event{ID: mkID(900, "A"), Ref: early.ID}
	skewed := Event{ID: mkID(200, "A"), Ref: late.ID}

	before, after := SplitAt(Sort([]Event{early, late, skewed}), 500)

	if len(before) != 1 || before[0].ID != early.ID {
		t.Fatalf("before = %v, want only the early event", ids(before))
	}
	if len(after) != 2 {
		t.Fatalf("after = %v, want the late event and its descendant", ids(after))
	}
	for _, e := range after {
		if e.ID == skewed.ID {
			return
		}
	}
	t.Fatal("the skewed descendant was replayed without its ancestor")
}

func TestSplitAtBoundaries(t *testing.T) {
	c := chain("A", "", 100, 3)

	if before, _ := SplitAt(c, 0); len(before) != 0 {
		t.Fatalf("cutoff before everything kept %d events", len(before))
	}
	if _, after := SplitAt(c, 1<<40); len(after) != 0 {
		t.Fatalf("cutoff after everything held back %d events", len(after))
	}
}

// An id that cannot be dated cannot be shown as part of a past state.
func TestSplitAtHoldsBackUndatableEvents(t *testing.T) {
	bad := Event{ID: "not-a-ulid"}
	before, after := SplitAt([]Event{bad}, 1<<40)

	if len(before) != 0 || len(after) != 1 {
		t.Fatalf("undatable event was not held back: %d before, %d after", len(before), len(after))
	}
}

func benchEvents(n int) []Event {
	// One long chain plus a handful of branches, the shape a few collaborators
	// syncing periodically actually produce.
	out := chain("A", "", 1, n*7/10)
	root := out[len(out)/2].ID
	out = append(out, chain("B", root, n, n*2/10)...)
	out = append(out, chain("C", root, n*2, n-len(out))...)
	return out
}

func BenchmarkSort(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		events := benchEvents(n)
		scratch := make([]Event, len(events))
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				// Restore the unsorted input each round; Sort works in place,
				// so reusing the result would measure the already-sorted case.
				b.StopTimer()
				copy(scratch, events)
				b.StartTimer()
				Sort(scratch)
			}
		})
	}
}
