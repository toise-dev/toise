package ingest

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// TestConformanceIntervalZeroNeverExpires locks the OTel 1.58.0 reading of
// entity.report.interval: "A value of 0 indicates that no periodic state events
// will be sent." Such an entity has no heartbeat cadence, so the liveness
// backstop MUST never arm — a value of 0 (or an absent interval) means the
// entity is only ever removed by an explicit entity.delete, never by Sweep.
//
// The spec also leaves receiver-side expiry permissive (it "can be used … to
// infer"), so Toise does not distinguish an asserted 0 from an absent interval:
// both mean "no cadence, never expire by Sweep". This test drives the full
// wire->engine path (routeRecord, not the engine directly) so a regression in
// either the convert boundary or the engine fails it.
func TestConformanceIntervalZeroNeverExpires(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	graph := projection.New()
	eng := change.New(graph, &failingAppender{}, change.WithClock(func() time.Time { return now }))

	// Three hosts that differ only in their entity.report.interval:
	//   h-zero    explicit interval 0  -> no cadence, must never expire
	//   h-absent  no interval at all   -> no cadence, must never expire
	//   h-armed   interval 60s         -> cadence armed, the control proving Sweep
	//                                     actually expires stale entities here
	withInterval := func(id string, secs int64, hasInterval bool) {
		logs := plog.NewLogs()
		lr := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName(evEntityState)
		lr.SetTimestamp(pcommon.NewTimestampFromTime(now))
		a := lr.Attributes()
		a.PutStr(attrEntityType, model.TypeHost)
		a.PutEmptyMap(attrEntityID).PutStr("host.id", id)
		if hasInterval {
			a.PutInt(attrEntityInterval, secs)
		}
		handled, dropped, err := routeRecord(eng, lr, "producer-1")
		if err != nil {
			t.Fatalf("ingest %s: %v", id, err)
		}
		if !handled {
			t.Fatalf("ingest %s: record not recognized as an entity event", id)
		}
		if len(dropped) != 0 {
			t.Fatalf("ingest %s: unexpected dropped keys %v", id, dropped)
		}
	}
	withInterval("h-zero", 0, true)
	withInterval("h-absent", 0, false)
	withInterval("h-armed", 60, true)

	if got := graph.EntityCount(); got != 3 {
		t.Fatalf("EntityCount = %d, want 3", got)
	}

	// Jump a full year past every plausible deadline and sweep. Only the armed
	// host may be expired; the two cadence-less hosts must survive untouched.
	now = now.Add(365 * 24 * time.Hour)
	n, err := eng.Sweep()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("Sweep expired %d entities, want exactly 1 (only the armed host)", n)
	}
	hostID := func(v string) []model.KeyValue {
		return []model.KeyValue{{Key: "host.id", Value: model.StringValue(v)}}
	}
	if _, found := graph.MatchIdentity(model.TypeHost, hostID("h-zero")); !found {
		t.Error("host with entity.report.interval=0 must never be expired by Sweep")
	}
	if _, found := graph.MatchIdentity(model.TypeHost, hostID("h-absent")); !found {
		t.Error("host with no entity.report.interval must never be expired by Sweep")
	}
	if _, found := graph.MatchIdentity(model.TypeHost, hostID("h-armed")); found {
		t.Error("host with an armed interval should have been expired by Sweep (control)")
	}

	// A second sweep, arbitrarily later still, must remain a no-op for the
	// cadence-less hosts: interval==0 is permanent, not a one-shot reprieve.
	now = now.Add(365 * 24 * time.Hour)
	if n, err := eng.Sweep(); err != nil || n != 0 {
		t.Fatalf("re-sweep = (%d, %v), want (0, nil): cadence-less entities must stay live", n, err)
	}
}
