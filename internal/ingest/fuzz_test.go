package ingest

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
)

// FuzzRouteRecord pins crash-safety at the ingest boundary: arbitrary
// attribute content must never panic the router — it either routes, rejects
// per record (errInvalidRecord), or ignores the record.
func FuzzRouteRecord(f *testing.F) {
	f.Add("entity.state", "host", "host.id", "h1", "os", "linux", int64(30))
	f.Add("entity.delete", "", "", "", "", "", int64(-1))
	f.Add("bogus.event", "ty\x00pe", "k", "", "entity.relationships", "not-a-slice", int64(1<<40))
	f.Fuzz(func(t *testing.T, eventName, typ, idKey, idVal, attrKey, attrVal string, interval int64) {
		lr := plog.NewLogRecord()
		lr.SetEventName(eventName)
		a := lr.Attributes()
		if typ != "" {
			a.PutStr("entity.type", typ)
		}
		if idKey != "" {
			a.PutEmptyMap("entity.id").PutStr(idKey, idVal)
		}
		if attrKey != "" {
			a.PutEmptyMap("entity.description").PutStr(attrKey, attrVal)
		}
		a.PutInt("entity.report.interval", interval)
		lr.SetTimestamp(1_700_000_000_000_000_000)

		eng := &fakeEngine{}
		if _, _, err := routeRecord(eng, lr, "fuzz-producer"); err != nil {
			// per-record rejection is a valid outcome; panics are the failure mode
			_ = err
		}
		r := newEmbeddedReconciler()
		if _, err := r.handle(eng, lr); err != nil {
			_ = err
		}
	})
}
