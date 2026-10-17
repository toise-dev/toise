package registry

import (
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/store"
)

// recorder collects reporter calls from any tenant's engine.
type recorder struct {
	mu             sync.Mutex
	discards       []string
	promises       []string
	discardTenants []string
	promiseTenants []string
}

func (r *recorder) dropped(tenantID, relationType, fromType, toType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discards = append(r.discards, tenantID+" "+relationType+" "+fromType+"->"+toType)
	r.discardTenants = append(r.discardTenants, tenantID)
}

func (r *recorder) refused(tenantID, producer, entityType string, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.promises = append(r.promises, tenantID+" "+producer+" "+entityType)
	r.promiseTenants = append(r.promiseTenants, tenantID)
}

// tenants returns the tenants each reporter named, sorted, so a test can assert
// WHICH tenants were attributed and not merely how many calls arrived.
func (r *recorder) tenants() (discards, promises []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	discards = append(discards, r.discardTenants...)
	promises = append(promises, r.promiseTenants...)
	sort.Strings(discards)
	sort.Strings(promises)
	return discards, promises
}

func (r *recorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.discards), len(r.promises)
}

// TestReportersReachTenantsOpenedAtBoot is the regression test for #525: the
// reporters were installed on the registry AFTER it had already opened every
// persisted tenant, and the engines took their callback by value at
// construction — so the counters stayed at zero for exactly the tenants that
// hold data. In production every tenant exists at boot, which made the series
// unconditionally zero while the WARN lines were being written.
//
// The test seeds a tenant directory so that boot opens it eagerly, installs the
// reporters only afterwards (the production order), and then provokes one
// discard and one refused promise on that stack. It asserts both halves: the
// boot-opened stack AND a stack minted later, because a fix that trades one for
// the other would still ship a counter that lies.
func TestReportersReachTenantsOpenedAtBoot(t *testing.T) {
	dataDir := t.TempDir()
	seedStore(t, filepath.Join(dataDir, "acme"), "h-seed")

	now := clock
	reg, err := openWithClock(dataDir, store.DefaultConfig(), time.Second, Limits{AutoCreate: true}, discard(),
		func() time.Time { return now })
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	if names := tenantNames(reg.Stacks()); !contains(names, "acme") {
		t.Fatalf("stacks = %v, want acme opened at boot — the test proves nothing otherwise", names)
	}

	rec := &recorder{}
	reg.SetDiscardReporter(rec.dropped)
	reg.SetPromiseReporter(rec.refused)

	boot, err := reg.For("acme")
	if err != nil {
		t.Fatalf("boot-opened tenant: %v", err)
	}
	later, err := reg.For("later")
	if err != nil {
		t.Fatalf("minting tenant: %v", err)
	}

	for _, st := range []*Stack{boot, later} {
		parkDoomedRelation(t, st.Engine, now)
		refuseImplausiblePromise(t, st.Engine, now)
	}

	// Past the reconciliation TTL the parked edges are given up on, which is the
	// only moment a discard is reported.
	now = now.Add(time.Minute)
	for _, st := range []*Stack{boot, later} {
		if _, err := st.Engine.Sweep(); err != nil {
			t.Fatalf("sweep %s: %v", st.Tenant, err)
		}
	}

	discards, promises := rec.counts()
	if discards != 2 {
		t.Errorf("discards reported = %d, want 2 (one per tenant); %v", discards, rec.discards)
	}
	if promises != 2 {
		t.Errorf("refused promises reported = %d, want 2 (one per tenant); %v", promises, rec.promises)
	}

	// The count was never the whole contract. A figure an operator cannot
	// attribute to a client sends them to the logs to find out, which is the
	// errand these instruments exist to spare them — so assert WHICH tenants
	// were named, not just that two calls arrived. This is also the assertion
	// that fails if every stack is handed the same closure: the counts would
	// still be 2 and 2, and both rows would say the same tenant.
	want := []string{"acme", "later"}
	gotDiscards, gotPromises := rec.tenants()
	if !reflect.DeepEqual(gotDiscards, want) {
		t.Errorf("discard tenants = %v, want %v", gotDiscards, want)
	}
	if !reflect.DeepEqual(gotPromises, want) {
		t.Errorf("promise tenants = %v, want %v", gotPromises, want)
	}
}

// parkDoomedRelation asserts an edge whose endpoints nothing ever emits, so the
// reconciliation buffer parks it and the next sweep past the TTL drops it.
func parkDoomedRelation(t *testing.T, eng *change.Engine, at time.Time) {
	t.Helper()
	if _, _, err := eng.ObserveRelation(change.RelationObservation{
		Type:      model.RelRunsOn,
		From:      change.EndpointRef{Type: model.TypeContainer, Identity: []model.KeyValue{{Key: "container.id", Value: model.StringValue("c-absent")}}},
		To:        change.EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h-absent")}}},
		EventTime: at,
	}); err != nil {
		t.Fatalf("observe doomed relation: %v", err)
	}
}

// refuseImplausiblePromise declares a cadence beyond what the engine accepts, so
// it refuses the promise and reports the producer defect.
func refuseImplausiblePromise(t *testing.T, eng *change.Engine, at time.Time) {
	t.Helper()
	if _, err := eng.ObserveEntity(change.EntityObservation{
		Type:      model.TypeHost,
		Identity:  []model.KeyValue{{Key: "host.id", Value: model.StringValue("h-liar")}},
		Producer:  "agent-liar",
		Interval:  9999 * time.Hour,
		EventTime: at,
	}); err != nil {
		t.Fatalf("observe host with an implausible interval: %v", err)
	}
}
