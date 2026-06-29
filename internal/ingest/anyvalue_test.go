package ingest

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

// TestAnyValueDescriptionFidelity drives a conformant producer record whose
// entity.description carries arrays and nested maps through the full path
// (ingest -> change engine -> store -> projection) and asserts the values are
// restored faithfully on both the live projection and the persisted event. A
// scalar-only producer is unaffected; this proves the new fidelity is lossless.
// See #259.
func TestAnyValueDescriptionFidelity(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	graph := projection.New()
	eng := change.New(graph, st)

	lr := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName(evEntityState)
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	a := lr.Attributes()
	a.PutStr(attrEntityType, model.TypeHost)
	a.PutEmptyMap(attrEntityID).PutStr("host.id", "h-any")
	desc := a.PutEmptyMap(attrEntityDesc)
	desc.PutStr("os.type", "linux")
	desc.PutInt("cpu.count", 8)
	tags := desc.PutEmptySlice("tags")
	tags.AppendEmpty().SetStr("edge")
	tags.AppendEmpty().SetStr("prod")
	net := desc.PutEmptyMap("net")
	net.PutInt("mtu", 1500)
	addrs := net.PutEmptySlice("addrs")
	addrs.AppendEmpty().SetStr("10.0.0.1")
	addrs.AppendEmpty().SetStr("10.0.0.2")

	handled, dropped, err := routeRecord(eng, lr, "p1")
	if err != nil || !handled {
		t.Fatalf("ingest: handled=%v err=%v", handled, err)
	}
	if len(dropped) != 0 {
		t.Fatalf("nothing should be dropped for a valid AnyValue, got %v", dropped)
	}

	want := map[string]string{
		"os.type":   "linux",
		"cpu.count": "8",
		"tags":      `["edge","prod"]`,
		"net":       `{"addrs":["10.0.0.1","10.0.0.2"],"mtu":1500}`,
	}

	// 1) live projection
	id, ok := graph.MatchIdentity(model.TypeHost, []model.KeyValue{{Key: "host.id", Value: model.StringValue("h-any")}})
	if !ok {
		t.Fatal("entity not found in projection")
	}
	ent, ok, _ := graph.GetEntity(id)
	if !ok {
		t.Fatal("GetEntity failed")
	}
	assertAttrs(t, "projection", ent.Attributes, want)

	// 2) persisted event (proto round-trip through the store)
	evs, err := st.ReadByType(context.Background(), model.EntityCreated)
	if err != nil {
		t.Fatalf("read created: %v", err)
	}
	if len(evs) != 1 || evs[0].Entity == nil {
		t.Fatalf("want one created event, got %d", len(evs))
	}
	assertAttrs(t, "store", evs[0].Entity.Entity.Attributes, want)
}

func assertAttrs(t *testing.T, where string, kvs []model.KeyValue, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, kv := range kvs {
		got[kv.Key] = kv.Value.Display()
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: attribute %q = %q, want %q", where, k, got[k], w)
		}
	}
}
