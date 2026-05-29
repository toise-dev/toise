package ingest

import (
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// BenchmarkRouteEntityState measures converting and dispatching an
// entity_state LogRecord (conversion only; excludes gRPC transport).
func BenchmarkRouteEntityState(b *testing.B) {
	lr := newRecord(evEntityState)
	a := lr.Attributes()
	a.PutStr(attrEntityType, model.TypeHost)
	a.PutEmptyMap(attrEntityID).PutStr("host.id", "h1")
	am := a.PutEmptyMap(attrEntityAttrs)
	am.PutStr("status", "up")
	am.PutInt("cpu.count", 8)

	f := &fakeEngine{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := routeRecord(f, lr); err != nil {
			b.Fatal(err)
		}
	}
}
