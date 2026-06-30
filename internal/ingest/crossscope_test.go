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

// addEntityState appends an entity.state record (optionally with embedded
// relationships) to a ScopeLogs, mirroring what a producer emits.
func addEntityState(sl plog.ScopeLogs, etype string, id map[string]string, rels []embRel) {
	lr := sl.LogRecords().AppendEmpty()
	lr.SetEventName(evEntityState)
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	a := lr.Attributes()
	a.PutStr(attrEntityType, etype)
	m := a.PutEmptyMap(attrEntityID)
	for k, v := range id {
		m.PutStr(k, v)
	}
	if len(rels) > 0 {
		slc := a.PutEmptySlice(attrEntityRelationships)
		for _, r := range rels {
			rm := slc.AppendEmpty().SetEmptyMap()
			rm.PutStr(relDescType, r.typ)
			rm.PutStr(relDescEntityType, r.toType)
			tm := rm.PutEmptyMap(relDescEntityID)
			for k, v := range r.toID {
				tm.PutStr(k, v)
			}
		}
	}
}

type embRel struct {
	typ, toType string
	toID        map[string]string
}

// runMultiScope drives one ResourceLogs (multiple ScopeLogs) through the exact
// path the receiver uses: a single engine.Batch, each record routed then its
// embedded relationships reconciled, in scope order.
func runMultiScope(t *testing.T, logs plog.Logs) *projection.Graph {
	t.Helper()
	g := projection.New()
	eng := change.New(g, &failingAppender{}, change.WithRelationBuffer(30*time.Second))
	recon := newEmbeddedReconciler()
	rls := logs.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		err := eng.Batch(func(b *change.Batch) {
			for j := 0; j < sls.Len(); j++ {
				recs := sls.At(j).LogRecords()
				for k := 0; k < recs.Len(); k++ {
					lr := recs.At(k)
					if _, _, err := routeRecordVocab(b, lr, "agent-1", true); err != nil {
						t.Fatalf("route: %v", err)
					}
					if _, err := recon.handleVocab(b, lr, true); err != nil {
						t.Fatalf("reconcile: %v", err)
					}
				}
			}
		})
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
	}
	return g
}

// TestCrossScopeRunsOnResolves reproduces the senhub-agent maintainer's report:
// a host-dep service.instance carries BOTH runs_on->host (host emitted in the
// foundation scope) and depends_on->network.endpoint (endpoint in the same
// host-dep scope). It checks whether the cross-scope runs_on survives — testing
// the suspicious order where the dependent's scope is processed BEFORE the host's.
func TestCrossScopeRunsOnResolves(t *testing.T) {
	hostID := map[string]string{"host.id": "H1"}
	svcID := map[string]string{"service.instance.id": "svchost.exe@H1"}
	epID := map[string]string{"server.address": "203.0.113.7", "server.port": "443", "network.transport": "tcp"}

	build := func(hostFirst bool) plog.Logs {
		logs := plog.NewLogs()
		rl := logs.ResourceLogs().AppendEmpty()
		rl.Resource().Attributes().PutStr(resAttrProducer, "agent-1")
		foundation := func() {
			sl := rl.ScopeLogs().AppendEmpty()
			sl.Scope().SetName("senhub-agent/foundation")
			addEntityState(sl, model.TypeHost, hostID, nil)
		}
		hostdep := func() {
			sl := rl.ScopeLogs().AppendEmpty()
			sl.Scope().SetName("senhub-agent/host-dep")
			addEntityState(sl, model.TypeNetworkEndpoint, epID, nil) // endpoint as its own entity
			addEntityState(sl, model.TypeServiceInstance, svcID, []embRel{
				{model.RelRunsOn, model.TypeHost, hostID},
				{model.RelDependsOn, model.TypeNetworkEndpoint, epID},
			})
		}
		if hostFirst {
			foundation()
			hostdep()
		} else {
			hostdep()
			foundation()
		}
		return logs
	}

	for _, tc := range []struct {
		name      string
		hostFirst bool
	}{
		{"host scope first", true},
		{"host-dep scope first (host arrives after the runs_on)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := runMultiScope(t, build(tc.hostFirst))
			runsOn := len(g.ListRelations(model.RelRunsOn, "", ""))
			dependsOn := len(g.ListRelations(model.RelDependsOn, "", ""))
			t.Logf("entities=%d relations=%d runs_on=%d depends_on=%d",
				g.EntityCount(), g.RelationCount(), runsOn, dependsOn)
			if dependsOn != 1 {
				t.Errorf("depends_on = %d, want 1 (same-scope endpoint)", dependsOn)
			}
			if runsOn != 1 {
				t.Errorf("runs_on = %d, want 1 — cross-scope host edge was DROPPED (reproduces the report)", runsOn)
			}
		})
	}
}
