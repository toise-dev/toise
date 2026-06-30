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

// hostDepExport builds a senhub-agent/host-dep export: the endpoint as its own
// entity, then the dependent service.instance carrying runs_on->host (host NOT
// in this export) + depends_on->endpoint (co-emitted here).
// setInterval stamps entity.report.interval (seconds) on the last record of sl.
func setInterval(sl plog.ScopeLogs, secs int64) {
	r := sl.LogRecords().At(sl.LogRecords().Len() - 1)
	r.Attributes().PutInt(attrEntityInterval, secs)
}

func hostDepExport(hostID string) plog.Logs {
	logs := plog.NewLogs()
	sl := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	sl.Scope().SetName("senhub-agent/host-dep")
	ep := map[string]string{"network.transport": "tcp", "server.address": "98.66.133.185", "server.port": "443"}
	addEntityState(sl, model.TypeNetworkEndpoint, ep, nil)
	addEntityState(sl, model.TypeServiceInstance, map[string]string{"service.instance.id": "svchost.exe@" + hostID},
		[]embRel{
			{model.RelRunsOn, model.TypeHost, map[string]string{"host.id": hostID}},
			{model.RelDependsOn, model.TypeNetworkEndpoint, ep},
		})
	setInterval(sl, 300) // the dependent carries report.interval=300 (5x cadence)
	return logs
}

func hostExport(hostID string) plog.Logs {
	logs := plog.NewLogs()
	sl := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	sl.Scope().SetName("senhub-agent/otlp-entities")
	addEntityState(sl, model.TypeHost, map[string]string{"host.id": hostID}, nil)
	setInterval(sl, 300)
	return logs
}

// TestHostDepParkingWindow guards the fix for toise-dev/toise#269: a host-dep
// export arrives without the host (heartbeat-suppressed that tick), so runs_on->host
// parks; the host's next heartbeat is ~2x cadence later. With a fixed buffer TTL
// (30s) shorter than that gap, Sweep used to drop the parked edge before the host
// arrived, and the dependent floated with only depends_on. The source carries
// report.interval=300, so the edge is now held for one re-emit cycle and survives
// until the host's heartbeat attaches it.
func TestHostDepParkingWindow(t *testing.T) {
	const H = "db17d891-46f2-4563-85d1-46402b1db900"
	t0 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	process := func(eng *change.Engine, recon *embeddedReconciler, logs plog.Logs) {
		rls := logs.ResourceLogs()
		_ = eng.Batch(func(b *change.Batch) {
			for i := 0; i < rls.Len(); i++ {
				sls := rls.At(i).ScopeLogs()
				for j := 0; j < sls.Len(); j++ {
					recs := sls.At(j).LogRecords()
					for k := 0; k < recs.Len(); k++ {
						lr := recs.At(k)
						lr.SetTimestamp(pcommon.NewTimestampFromTime(timeNow()))
						_, _, _ = routeRecordVocab(b, lr, "agent-1", true)
						_, _ = recon.handleVocab(b, lr, true)
					}
				}
			}
		})
	}
	_ = process

	run := func(t *testing.T, hostFirst bool, gap time.Duration) (runsOn, dependsOn int) {
		now := t0
		g := projection.New()
		eng := change.New(g, &failingAppender{},
			change.WithRelationBuffer(30*time.Second),
			change.WithClock(func() time.Time { return now }))
		recon := newEmbeddedReconciler()
		proc := func(logs plog.Logs) {
			rls := logs.ResourceLogs()
			_ = eng.Batch(func(b *change.Batch) {
				for i := 0; i < rls.Len(); i++ {
					sls := rls.At(i).ScopeLogs()
					for j := 0; j < sls.Len(); j++ {
						recs := sls.At(j).LogRecords()
						for k := 0; k < recs.Len(); k++ {
							lr := recs.At(k)
							lr.SetTimestamp(pcommon.NewTimestampFromTime(now))
							_, _, _ = routeRecordVocab(b, lr, "agent-1", true)
							_, _ = recon.handleVocab(b, lr, true)
						}
					}
				}
			})
		}
		if hostFirst {
			proc(hostExport(H))
		}
		proc(hostDepExport(H)) // dependent with runs_on->host; host not in this export
		if !hostFirst {
			// host heartbeat arrives `gap` later; a periodic Sweep runs in between
			now = now.Add(gap)
			_, _ = eng.Sweep()
			proc(hostExport(H))
		}
		return len(g.ListRelations(model.RelRunsOn, "", "")), len(g.ListRelations(model.RelDependsOn, "", ""))
	}

	t.Run("host live first (steady state)", func(t *testing.T) {
		ro, do := run(t, true, 0)
		t.Logf("runs_on=%d depends_on=%d", ro, do)
		if ro != 1 || do != 1 {
			t.Errorf("steady state: runs_on=%d depends_on=%d, want 1/1", ro, do)
		}
	})
	t.Run("host-dep first, host 120s later (gap > 30s TTL)", func(t *testing.T) {
		ro, do := run(t, false, 120*time.Second)
		t.Logf("runs_on=%d depends_on=%d", ro, do)
		if do != 1 {
			t.Errorf("depends_on=%d, want 1 (endpoint co-emitted)", do)
		}
		// The fix: source report.interval=300 holds the parked runs_on past the 120s
		// gap, so the host's heartbeat attaches it instead of Sweep dropping it.
		if ro != 1 {
			t.Errorf("runs_on=%d, want 1 — parked edge dropped before the host's heartbeat (regression of #269)", ro)
		}
	})
}

func timeNow() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }
