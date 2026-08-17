package event

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shakfu/gnotes/internal/ulid"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	g := ulid.NewGenerator()
	want := Event{
		ID:     g.New(),
		Ref:    g.New(),
		Action: AddTask,
		Payload: Payload{
			ID:       "01ABCDEFGHJKMNPQRSTVWXYZ00",
			Parent:   "01ABCDEFGHJKMNPQRSTVWXYZ01",
			Rank:     "7fffffffffffffffffffffff",
			Title:    "fix the lexer",
			Status:   "open",
			Priority: "high",
		},
	}

	line, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(line)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if got.ID != want.ID || got.Ref != want.Ref || got.Action != want.Action {
		t.Fatalf("header mismatch: got %+v", got)
	}
	if !reflect.DeepEqual(got.Payload, want.Payload) {
		t.Fatalf("payload = %+v, want %+v", got.Payload, want.Payload)
	}
}

// A line must carry only the fields its action uses, or a long log pays for
// every unused field on every record.
func TestEncodeOmitsEmptyFields(t *testing.T) {
	g := ulid.NewGenerator()
	line, err := Encode(Event{ID: g.New(), Action: DeleteNode, Payload: Payload{ID: "x"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, absent := range []string{"parent", "rank", "title", "md", "tag", "assignee", "status", "due", "priority", "target", "ranks", "ref"} {
		if strings.Contains(string(line), `"`+absent+`"`) {
			t.Errorf("line carries empty field %q: %s", absent, line)
		}
	}
	for _, present := range []string{`"v":1`, `"a":"delete.node"`, `"id":`} {
		if !strings.Contains(string(line), present) {
			t.Errorf("line is missing %s: %s", present, line)
		}
	}
}

// The author is not written on the line; it comes from the filename. Storing
// it twice would let the two disagree.
func TestEncodeDoesNotStoreAuthor(t *testing.T) {
	g := ulid.NewGenerator()
	line, err := Encode(Event{ID: g.New(), Action: AddNote, UserID: "USER1", UserName: "sa"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), "USER1") || strings.Contains(string(line), `"sa"`) {
		t.Fatalf("line leaked author fields: %s", line)
	}
}

func TestEncodeRebalanceCarriesRanks(t *testing.T) {
	g := ulid.NewGenerator()
	want := map[string]string{"a": "0001", "b": "0002"}

	line, err := Encode(Event{ID: g.New(), Action: Rebalance, Payload: Payload{Parent: "p", Ranks: want}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Payload.Ranks) != 2 || got.Payload.Ranks["a"] != "0001" || got.Payload.Ranks["b"] != "0002" {
		t.Fatalf("ranks = %v, want %v", got.Payload.Ranks, want)
	}
}

// A newer gnotes may write actions this build has never heard of. Replay must
// step over them, not refuse the whole log.
func TestDecodeReportsUnknownActionDistinctly(t *testing.T) {
	g := ulid.NewGenerator()
	line := []byte(`{"v":1,"id":"` + g.New() + `","a":"teleport.node","node":"x"}`)

	got, err := Decode(line)

	var unknown *ErrUnknownAction
	if !errors.As(err, &unknown) {
		t.Fatalf("Decode = %v, want ErrUnknownAction", err)
	}
	if unknown.Action != "teleport.node" {
		t.Fatalf("reported action %q", unknown.Action)
	}
	// The header still parses, so a caller can log which record it skipped.
	if got.ID == "" {
		t.Fatal("unknown-action event lost its id")
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	g := ulid.NewGenerator()
	good := g.New()

	cases := map[string]string{
		"not json":         `{nope`,
		"wrong version":    `{"v":99,"id":"` + good + `","a":"add.note"}`,
		"missing version":  `{"id":"` + good + `","a":"add.note"}`,
		"bad event id":     `{"v":1,"id":"nope","a":"add.note"}`,
		"bad ref":          `{"v":1,"id":"` + good + `","ref":"nope","a":"add.note"}`,
		"payload wrong ty": `{"v":1,"id":"` + good + `","a":"add.note","title":42}`,
		"ranks wrong ty":   `{"v":1,"id":"` + good + `","a":"rebalance.children","ranks":"nope"}`,
	}

	for name, line := range cases {
		if _, err := Decode([]byte(line)); err == nil {
			t.Errorf("%s: Decode returned nil error", name)
		}
	}
}

func TestDecodeAcceptsAbsentPayload(t *testing.T) {
	g := ulid.NewGenerator()
	got, err := Decode([]byte(`{"v":1,"id":"` + g.New() + `","a":"delete.node"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Action != DeleteNode {
		t.Fatalf("action = %q", got.Action)
	}
}

func TestKnownCoversEveryDeclaredAction(t *testing.T) {
	declared := []Action{
		InitWorkspace, AddNotebook, AddNote, AddTask, MoveNode, DeleteNode,
		RestoreNode, Rebalance, EditTitle, EditBody, SetStatus, SetDue,
		SetPriority, AddAssignee, RemoveAssignee, LinkNode, UnlinkNode,
		AddTag, RemoveTag, CreateContributor, RenameContributor,
	}

	for _, a := range declared {
		if !Known(a) {
			t.Errorf("declared action %q is missing from the known registry", a)
		}
	}
	if len(known) != len(declared) {
		t.Errorf("registry has %d actions, %d are declared; one side was not updated", len(known), len(declared))
	}
	if Known("nonsense.action") {
		t.Error("Known accepted an undeclared action")
	}
}

// The on-disk shape is a compatibility promise. Pin it so a careless struct
// tag edit fails loudly here rather than silently orphaning existing logs.
func TestWireFormatIsStable(t *testing.T) {
	line, err := Encode(Event{
		ID:      "01ABCDEFGHJKMNPQRSTVWXYZ00",
		Ref:     "01ABCDEFGHJKMNPQRSTVWXYZ01",
		Action:  AddTask,
		Payload: Payload{ID: "n1", Parent: "p1", Title: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const want = `{"v":1,"id":"01ABCDEFGHJKMNPQRSTVWXYZ00","ref":"01ABCDEFGHJKMNPQRSTVWXYZ01","a":"add.task","node":"n1","parent":"p1","title":"t"}`
	if string(line) != want {
		t.Fatalf("wire format changed:\n got %s\nwant %s", line, want)
	}
}

// The payload is inlined into the envelope, and the node reference must not
// collide with the event's own id.
func TestPayloadIsFlattenedWithoutColliding(t *testing.T) {
	g := ulid.NewGenerator()
	eventID := g.New()
	line, err := Encode(Event{ID: eventID, Action: AddNote, Payload: Payload{ID: "n1", Title: "x"}})
	if err != nil {
		t.Fatal(err)
	}

	var probe map[string]any
	if err := json.Unmarshal(line, &probe); err != nil {
		t.Fatal(err)
	}
	if _, nested := probe["p"]; nested {
		t.Fatalf("payload is still nested: %s", line)
	}
	if probe["id"] != eventID {
		t.Fatalf("\"id\" = %v, want the event id %q", probe["id"], eventID)
	}
	if probe["node"] != "n1" {
		t.Fatalf("\"node\" = %v, want the payload node id", probe["node"])
	}
	if probe["title"] != "x" {
		t.Fatalf("\"title\" = %v", probe["title"])
	}
}

func BenchmarkDecode(b *testing.B) {
	g := ulid.NewGenerator()
	line, _ := Encode(Event{
		ID: g.New(), Ref: g.New(), Action: AddTask,
		Payload: Payload{ID: "n", Parent: "p", Rank: "7fffffffffffffffffffffff", Title: "fix the lexer"},
	})
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		if _, err := Decode(line); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	g := ulid.NewGenerator()
	e := Event{
		ID: g.New(), Ref: g.New(), Action: AddTask,
		Payload: Payload{ID: "n", Parent: "p", Rank: "7fffffffffffffffffffffff", Title: "fix the lexer"},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Encode(e); err != nil {
			b.Fatal(err)
		}
	}
}
