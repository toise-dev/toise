package ingest

import (
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

func exportAs(t *testing.T, client plogotlp.GRPCClient, producer, hostID string) {
	t.Helper()
	exportAsTenant(t, client, "", producer, hostID)
}

func exportAsTenant(t *testing.T, client plogotlp.GRPCClient, tenantID, producer, hostID string) {
	t.Helper()
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	res := rl.Resource()
	res.Attributes().PutStr(resAttrProducer, producer)
	if hostID != "" {
		res.Attributes().PutStr("host.id", hostID)
	}
	if tenantID != "" {
		res.Attributes().PutStr(tenant.ResourceAttr, tenantID)
	}
	sl := rl.ScopeLogs().AppendEmpty()
	hid := hostID
	if hid == "" {
		hid = "host-x"
	}
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": hid}, map[string]string{"status": "up"})
	embeddedEntity(sl, t0, model.TypeProcess, map[string]string{"pid": "100"},
		[]relDesc{{relType: model.RelRunsOn, toType: model.TypeHost, toID: map[string]string{"host.id": hid}}})
	export(t, client, ld)
}

func TestSharedInstanceIDDoesNotRetractOtherMachinesEdge(t *testing.T) {
	client, graph, _ := startReceiver(t)

	exportAs(t, client, "agent-a", "host-a")
	exportAs(t, client, "agent-a", "host-b")
	exportAs(t, client, "agent-a", "host-a")

	if got := graph.RelationCount(); got != 2 {
		t.Fatalf("RelationCount() = %d, want 2", got)
	}
}

func TestSharedInstanceIDCounterIncrements(t *testing.T) {
	base := t.TempDir()
	var mu sync.Mutex
	engines := map[string]*change.Engine{}
	engineFor := func(id string) (*change.Engine, error) {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := engines[id]; ok {
			return e, nil
		}
		st, err := store.Open(filepath.Join(base, id), store.DefaultConfig())
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = st.Close() })
		engines[id] = change.New(projection.New(), st, change.WithClock(func() time.Time { return t0 }))
		return engines[id], nil
	}
	m := NewMetrics()
	rec := NewRoutedReceiver(engineFor, nil, m, false, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	go func() { _ = rec.Serve(lis) }()
	t.Cleanup(rec.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := plogotlp.NewGRPCClient(conn)

	exportAsTenant(t, client, "tenant-a", "agent-a", "host-a")
	counts := readCounters(t, m)
	if got := counter(t, counts, "toise_ingest_producer_conflicts_total", "shared_instance_id", "tenant-a"); got != 0 {
		t.Fatalf("conflict counter after first export = %v, want 0", got)
	}

	exportAsTenant(t, client, "tenant-a", "agent-a", "host-b")
	counts = readCounters(t, m)
	if got := counter(t, counts, "toise_ingest_producer_conflicts_total", "shared_instance_id", "tenant-a"); got < 1 {
		t.Fatalf("conflict counter after second export = %v, want >= 1", got)
	}

	hosts := rec.ConflictsFor("tenant-a").ConflictingHosts("agent-a")
	if len(hosts) != 2 || hosts[0] != "host-a" || hosts[1] != "host-b" {
		t.Fatalf("ConflictingHosts(\"agent-a\") = %v, want [host-a host-b]", hosts)
	}
}

func TestSingleMoveDoesNotLeavePermanentConflict(t *testing.T) {
	d := newConflictDetector()
	now := t0
	d.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if c, w := d.observe("agent-a", "host-a"); c || w {
			t.Fatalf("observe host-a: conflicting=%v warn=%v, want false false", c, w)
		}
		now = now.Add(30 * time.Second)
	}

	if c, w := d.observe("agent-a", "host-b"); !c || !w {
		t.Fatalf("first observe host-b: conflicting=%v warn=%v, want true true", c, w)
	}
	hosts := d.ConflictingHosts("agent-a")
	if len(hosts) != 2 || hosts[0] != "host-a" || hosts[1] != "host-b" {
		t.Fatalf("ConflictingHosts(\"agent-a\") = %v, want [host-a host-b]", hosts)
	}

	var warnTimes []time.Time
	if c, w := d.observe("agent-a", "host-a"); c && w {
		warnTimes = append(warnTimes, now)
	}
	for i := 0; i < 9; i++ {
		now = now.Add(30 * time.Second)
		if c, w := d.observe("agent-a", "host-b"); c && w {
			warnTimes = append(warnTimes, now)
		}
	}

	now = now.Add(conflictWindow + time.Minute)
	if c, w := d.observe("agent-a", "host-b"); c || w {
		t.Fatalf("observe host-b after window: conflicting=%v warn=%v, want false false", c, w)
	}
	if hosts := d.ConflictingHosts("agent-a"); hosts != nil {
		t.Fatalf("ConflictingHosts(\"agent-a\") = %v after window, want nil", hosts)
	}

	for i := 0; i < len(warnTimes)-1; i++ {
		if warnTimes[i+1].Sub(warnTimes[i]) < conflictWindow {
			t.Fatalf("warn returned twice within one window: %v and %v", warnTimes[i], warnTimes[i+1])
		}
	}
}

func TestNoHostIDKeepsReconciliationUnchanged(t *testing.T) {
	client, graph, _ := startReceiver(t)

	exportAs(t, client, "agent-a", "")
	if got := graph.RelationCount(); got != 1 {
		t.Fatalf("RelationCount() after first export = %d, want 1", got)
	}

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr(resAttrProducer, "agent-a")
	sl := rl.ScopeLogs().AppendEmpty()
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "host-x"}, map[string]string{"status": "up"})
	embeddedEntity(sl, t0, model.TypeProcess, map[string]string{"pid": "100"}, nil)
	export(t, client, ld)

	if got := graph.RelationCount(); got != 0 {
		t.Fatalf("RelationCount() after second export = %d, want 0", got)
	}

	d := newConflictDetector()
	if c, w := d.observe("agent-a", ""); c || w {
		t.Fatalf("observe with empty hostID: conflicting=%v warn=%v, want false false", c, w)
	}
	if hosts := d.ConflictingHosts("agent-a"); hosts != nil {
		t.Fatalf("ConflictingHosts(\"agent-a\") = %v, want nil", hosts)
	}
}

func TestStaggeredClonesAreDetected(t *testing.T) {
	d := newConflictDetector()
	now := t0
	d.now = func() time.Time { return now }

	// 4 cycles; host-b lags host-a by 6 minutes on a 10-minute cadence.
	obs := 0
	for cycle := 0; cycle < 4; cycle++ {
		if c, _ := d.observe("agent-a", "host-a"); obs > 0 && !c {
			t.Fatalf("observation %d (host-a): conflicting=%v, want true", obs, c)
		}
		obs++
		now = now.Add(6 * time.Minute)
		if c, _ := d.observe("agent-a", "host-b"); obs > 0 && !c {
			t.Fatalf("observation %d (host-b): conflicting=%v, want true", obs, c)
		}
		obs++
		now = now.Add(4 * time.Minute)
	}
}
