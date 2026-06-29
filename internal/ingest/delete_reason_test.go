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

// TestDeleteReasonCaptured asserts that entity.delete.reason is parsed from the
// wire, carried onto the entity.deleted event, and persisted — and that its
// absence is not an error (purely additive, open enum). See #260.
func TestDeleteReasonCaptured(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := change.New(projection.New(), st)

	stateThenDelete := func(hostID, reason string, withReason bool) {
		state := newHostRecord(hostID, "", false)
		if _, _, err := routeRecord(eng, state, "p1"); err != nil {
			t.Fatalf("state %s: %v", hostID, err)
		}
		del := newHostRecord(hostID, reason, withReason)
		del.SetEventName(evEntityDelete)
		if _, _, err := routeRecord(eng, del, "p1"); err != nil {
			t.Fatalf("delete %s: %v", hostID, err)
		}
	}
	stateThenDelete("h-1", "terminated", true)
	stateThenDelete("h-2", "", false) // no reason given -> must stay empty, no error

	evs, err := st.ReadByType(context.Background(), model.EntityDeleted)
	if err != nil {
		t.Fatalf("read deletes: %v", err)
	}
	got := map[string]string{}
	for _, ev := range evs {
		if ev.Entity == nil {
			continue
		}
		got[ev.Entity.Entity.Identity[0].Value.Display()] = ev.Entity.DeleteReason
	}
	if got["h-1"] != "terminated" {
		t.Errorf("h-1 delete reason = %q, want %q", got["h-1"], "terminated")
	}
	if got["h-2"] != "" {
		t.Errorf("h-2 delete reason = %q, want empty (none was sent)", got["h-2"])
	}
}

// TestDeleteReasonOpenEnum confirms an unrecognized reason is accepted verbatim:
// Toise never validates entity.delete.reason against a closed set.
func TestDeleteReasonOpenEnum(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := change.New(projection.New(), st)

	const custom = "decommissioned-by-runbook-42" // not in the spec's example set
	if _, _, err := routeRecord(eng, newHostRecord("h-x", "", false), "p1"); err != nil {
		t.Fatal(err)
	}
	del := newHostRecord("h-x", custom, true)
	del.SetEventName(evEntityDelete)
	if _, _, err := routeRecord(eng, del, "p1"); err != nil {
		t.Fatalf("delete with custom reason must be accepted: %v", err)
	}
	evs, err := st.ReadByType(context.Background(), model.EntityDeleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Entity == nil || evs[0].Entity.DeleteReason != custom {
		t.Fatalf("custom reason not preserved verbatim: %+v", evs)
	}
}

// newHostRecord builds an entity.state host LogRecord, optionally carrying an
// entity.delete.reason (only meaningful once the caller flips it to a delete).
func newHostRecord(hostID, reason string, withReason bool) plog.LogRecord {
	lr := plog.NewLogs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName(evEntityState)
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	a := lr.Attributes()
	a.PutStr(attrEntityType, model.TypeHost)
	a.PutEmptyMap(attrEntityID).PutStr("host.id", hostID)
	if withReason {
		a.PutStr(attrEntityDeleteReason, reason)
	}
	return lr
}
