package ulid

import (
	"sort"
	"testing"
	"time"
)

func TestNewIsValidAndWellFormed(t *testing.T) {
	g := NewGenerator()
	id := g.New()

	if len(id) != Len {
		t.Fatalf("length = %d, want %d", len(id), Len)
	}
	if !Valid(id) {
		t.Fatalf("Valid(%q) = false", id)
	}
}

func TestTimeRoundTrip(t *testing.T) {
	want := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	g := NewGeneratorAt(func() time.Time { return want })

	got, err := Timestamp(g.New())
	if err != nil {
		t.Fatalf("Timestamp: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("Timestamp = %v, want %v", got, want)
	}
}

// The generator must stay strictly ascending even when every call lands in the
// same millisecond, because gnotes writes several events per user operation.
func TestMonotonicWithinOneMillisecond(t *testing.T) {
	frozen := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	g := NewGeneratorAt(func() time.Time { return frozen })

	const n = 1000
	ids := make([]string, n)
	for i := range ids {
		ids[i] = g.New()
	}

	for i := 1; i < n; i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("id %d (%s) does not sort after id %d (%s)", i, ids[i], i-1, ids[i-1])
		}
	}
}

// A clock that jumps backwards must not produce IDs that sort before ones
// already written, or replay order would silently change.
func TestClockGoingBackwardsStaysMonotonic(t *testing.T) {
	times := []time.Time{
		time.Date(2026, 8, 17, 12, 0, 5, 0, time.UTC),
		time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 17, 12, 0, 1, 0, time.UTC),
	}
	i := 0
	g := NewGeneratorAt(func() time.Time {
		t := times[i]
		i++
		return t
	})

	a, b, c := g.New(), g.New(), g.New()
	if !(a < b && b < c) {
		t.Fatalf("not ascending across a backwards clock: %s %s %s", a, b, c)
	}
}

// NewAfter is how a machine with a lagging clock catches up to a log written
// elsewhere. The result must sort after the supplied floor.
func TestNewAfterRespectsFloor(t *testing.T) {
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	g := NewGeneratorAt(func() time.Time { return past })

	future := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	floor := uint64(future.UnixMilli())

	id := g.NewAfter(floor)
	ms, err := Time(id)
	if err != nil {
		t.Fatalf("Time: %v", err)
	}
	if ms < floor {
		t.Fatalf("timestamp %d is below floor %d", ms, floor)
	}
}

// Lexicographic order must match chronological order; the causal sort depends
// on it for tiebreaking concurrent events.
func TestLexicographicOrderMatchesChronological(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	offsets := []int{500, 3, 12000, 0, 7, 99999}

	var ids []string
	for _, off := range offsets {
		at := base.Add(time.Duration(off) * time.Millisecond)
		g := NewGeneratorAt(func() time.Time { return at })
		ids = append(ids, g.New())
	}

	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)

	for i := 1; i < len(sorted); i++ {
		prev, err := Time(sorted[i-1])
		if err != nil {
			t.Fatal(err)
		}
		cur, err := Time(sorted[i])
		if err != nil {
			t.Fatal(err)
		}
		if prev > cur {
			t.Fatalf("string order disagrees with time order at %d: %d > %d", i, prev, cur)
		}
	}
}

func TestValidRejectsMalformed(t *testing.T) {
	g := NewGenerator()
	good := g.New()

	cases := map[string]string{
		"empty":              "",
		"too short":          good[:25],
		"too long":           good + "0",
		"ambiguous letter I": "0" + "I" + good[2:],
		"ambiguous letter U": "0" + "U" + good[2:],
		"timestamp overflow": "8" + good[1:],
		"non-base32 punct":   "0-" + good[2:],
	}

	for name, in := range cases {
		if Valid(in) {
			t.Errorf("%s: Valid(%q) = true, want false", name, in)
		}
	}
}

func TestValidAcceptsLowercase(t *testing.T) {
	g := NewGenerator()
	id := g.New()

	lower := ""
	for _, r := range id {
		if r >= 'A' && r <= 'Z' {
			lower += string(r + 32)
		} else {
			lower += string(r)
		}
	}

	if !Valid(lower) {
		t.Fatalf("Valid(%q) = false, want true", lower)
	}
	if got := Canonical(lower); got != id {
		t.Fatalf("Canonical(%q) = %q, want %q", lower, got, id)
	}
}

func TestCanonicalLeavesNonULIDAlone(t *testing.T) {
	const in = "not a ulid at all......"
	if got := Canonical(in); got != in {
		t.Fatalf("Canonical(%q) = %q, want unchanged", in, got)
	}
}

func TestTimeRejectsInvalid(t *testing.T) {
	if _, err := Time("nope"); err == nil {
		t.Fatal("Time on invalid input returned nil error")
	}
}

func TestShort(t *testing.T) {
	g := NewGenerator()
	id := g.New()

	if got := Short(id, 6); got != id[20:] {
		t.Fatalf("Short = %q, want %q", got, id[20:])
	}
	if got := Short("abc", 6); got != "abc" {
		t.Fatalf("Short of a shorter string = %q, want %q", got, "abc")
	}
}

// Distinct generators must not collide, since every user writes to their own
// log and the merged result is keyed by event id.
func TestNoCollisionsAcrossGenerators(t *testing.T) {
	frozen := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	seen := make(map[string]bool)

	for g := 0; g < 8; g++ {
		gen := NewGeneratorAt(func() time.Time { return frozen })
		for i := 0; i < 500; i++ {
			id := gen.New()
			if seen[id] {
				t.Fatalf("duplicate id %s", id)
			}
			seen[id] = true
		}
	}
}

func BenchmarkNew(b *testing.B) {
	g := NewGenerator()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.New()
	}
}

func BenchmarkTime(b *testing.B) {
	g := NewGenerator()
	id := g.New()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Time(id)
	}
}
