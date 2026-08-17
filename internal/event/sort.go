package event

import (
	"slices"
	"strings"

	"github.com/shakfu/gnotes/internal/ulid"
)

// Sort reorders events into the single canonical order that every machine
// agrees on, given the same set of events in any input order. It sorts in
// place and returns the same slice.
//
// Each event names the last event its author had seen, so the refs form a tree
// rooted at the first event ever written. A depth-first preorder walk of that
// tree replays each author's work as a contiguous run, in the order they
// intended, and the walk visits branches in ULID order so two authors who
// diverged from the same point are always interleaved the same way.
//
// Ordering by timestamp alone would not do this: clocks disagree between
// machines, so the same two logs could replay differently depending on whose
// clock was ahead. The refs make the order a property of the data rather than
// of the machine reading it.
//
// Events whose ref names something not present are attached as additional
// roots, in ULID order, after the real tree. That happens whenever a log is
// pulled before the log it branched from, and it must not lose events; a later
// sync that brings in the missing ancestor simply moves them back into place.
//
// Sorting in place matters at scale. Event is a wide struct, so returning a
// fresh slice would copy tens of megabytes for a large log, which dominated
// every other cost in this function. Callers that need their input preserved
// should copy it first.
func Sort(events []Event) []Event {
	if len(events) < 2 {
		return events
	}
	n := len(events)

	// order lists every event index grouped by ref and ascending by id within
	// each group. Sorting once and reading contiguous spans out of the result
	// replaces a map of per-ref slices, which allocated once per distinct ref
	// and was the bulk of this function's garbage.
	order := make([]int32, n)
	for i := range order {
		order[i] = int32(i)
	}
	// slices.SortFunc over int32 rather than sort.Slice, which reaches through
	// reflect to swap and costs several times as much on a large log.
	slices.SortFunc(order, func(a, b int32) int {
		x, y := &events[a], &events[b]
		if c := strings.Compare(x.Ref, y.Ref); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})

	// span records where each ref's children begin and end inside order.
	type span struct{ lo, hi int32 }
	children := make(map[string]span, n)
	for i := 0; i < n; {
		ref := events[order[i]].Ref
		j := i + 1
		for j < n && events[order[j]].Ref == ref {
			j++
		}
		children[ref] = span{int32(i), int32(j)}
		i = j
	}

	// dest[i] is the position event i must end up at. The walk records
	// destinations rather than emitting events, and the whole permutation is
	// applied to the slice in one pass at the end.
	dest := make([]int32, n)
	placed := make([]bool, n)
	next := int32(0)

	// An explicit stack rather than recursion. The common shape of this tree
	// is not bushy but a single chain as long as the log, because each event
	// refs the one before it, so a recursive walk would nest once per event.
	stack := make([]int32, 0, 64)

	visit := func(start int32) {
		stack = append(stack[:0], start)
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if placed[i] {
				continue
			}
			placed[i] = true
			dest[i] = next
			next++

			// Push in reverse so the lowest ULID is popped first and the
			// output stays in ascending sibling order.
			if s, ok := children[events[i].ID]; ok {
				for k := s.hi - 1; k >= s.lo; k-- {
					if c := order[k]; !placed[c] {
						stack = append(stack, c)
					}
				}
			}
		}
	}

	// Real roots: events written with no prior event to refer to.
	if s, ok := children[""]; ok {
		for k := s.lo; k < s.hi; k++ {
			visit(order[k])
		}
	}

	// Everything reachable from a real root is now placed. What remains is
	// either an orphan, whose ref names an event we do not have, or a member
	// of a ref cycle. Both are visited in ULID order, the same tiebreak the
	// tree walk uses, so the result does not depend on which partial set of
	// logs happened to be present.
	//
	// An orphan run must be entered at its head rather than at whichever of
	// its events has the lowest id, or a run whose ids descend would replay
	// backwards. Taking orphans before cycle members achieves that: every
	// event in the run except the head refs another event in the run, so only
	// the head qualifies as an orphan.
	if int(next) < n {
		rest := make([]int32, 0, n-int(next))
		for i := range events {
			if !placed[i] {
				rest = append(rest, int32(i))
			}
		}
		sortByID(events, rest)

		// Only needed to tell an orphan from a cycle member, and both are
		// rare, so the set is built here rather than on every load.
		exists := make(map[string]struct{}, n)
		for i := range events {
			exists[events[i].ID] = struct{}{}
		}

		for _, i := range rest {
			if _, haveRef := exists[events[i].Ref]; !haveRef {
				visit(i)
			}
		}
		// Anything still unplaced is in a cycle, which a correct writer cannot
		// produce, since an event's ref always names an event that already
		// existed. Handling it anyway means a corrupted or hand-edited log
		// degrades to a stable order instead of silently dropping records.
		for _, i := range rest {
			if !placed[i] {
				visit(i)
			}
		}
	}

	permute(events, dest)
	return events
}

// sortByID orders a slice of indices by the ULID of the event each points to.
func sortByID(events []Event, idx []int32) {
	slices.SortFunc(idx, func(a, b int32) int {
		return strings.Compare(events[a].ID, events[b].ID)
	})
}

// permute rearranges events so that the element at index i moves to dest[i],
// using cycle decomposition. Every element is written at most once and the
// only scratch space is a single Event, which is what keeps a large log from
// allocating a second copy of itself.
//
// dest is consumed: it is rewritten to the identity as the cycles close.
func permute(events []Event, dest []int32) {
	for i := 0; i < len(dest); i++ {
		for dest[i] != int32(i) {
			j := dest[i]
			events[i], events[j] = events[j], events[i]
			dest[i], dest[j] = dest[j], dest[i]
		}
	}
}

// Sorted returns a canonically ordered copy, leaving the input untouched. Use
// it when the caller does not own the slice; otherwise prefer Sort.
func Sorted(events []Event) []Event {
	return Sort(append([]Event(nil), events...))
}

// EdgeRef returns the id of the last event in a sorted log, which a new event
// takes as its ref. Passing an unsorted log would produce an arbitrary
// reference and a tree that reshapes on the next load.
func EdgeRef(sorted []Event) string {
	if len(sorted) == 0 {
		return ""
	}
	return sorted[len(sorted)-1].ID
}

// SplitAt divides a sorted log into the events at or before cutoffMS and those
// after it, for viewing the state as it stood at a past moment.
//
// An event is held back when its own timestamp is past the cutoff, and also
// when anything it refs was held back. That second rule is what makes the
// result a state the data actually passed through: a clock that ran ahead
// could otherwise let an event through while its causal ancestor was excluded,
// materialising a mix that never existed. Ordering already guarantees an
// event's ref precedes it, so one forward pass decides every event.
func SplitAt(sorted []Event, cutoffMS uint64) (before, after []Event) {
	var held map[string]struct{}

	for _, e := range sorted {
		ms, err := ulid.Time(e.ID)
		// An id that will not decode cannot be dated, so it cannot be shown as
		// part of a past state. Holding it back is the conservative choice.
		keep := err == nil && ms <= cutoffMS

		if _, refHeld := held[e.Ref]; !keep || (e.Ref != "" && refHeld) {
			if held == nil {
				held = make(map[string]struct{})
			}
			held[e.ID] = struct{}{}
			after = append(after, e)
			continue
		}
		before = append(before, e)
	}
	return before, after
}
