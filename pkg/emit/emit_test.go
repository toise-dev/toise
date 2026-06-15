package emit

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"

	"github.com/toise-dev/toise/pkg/emit/conformance"
)

var update = flag.Bool("update", false, "rewrite golden fixtures")

func fixedClock() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }

// fixtureClient builds the canonical scenario behind the published fixture:
// one host with an interval, one listener running on it, one delete.
func fixtureClient(t *testing.T) (*Client, []Entity, []Entity) {
	t.Helper()
	c := &Client{opts: Options{
		ServiceName:       "fixture-producer",
		ServiceInstanceID: "fixture-01",
		Resource:          map[string]string{"host.id": "0f7a3c1e-aaaa-bbbb-cccc-000000000001"},
	}, now: fixedClock}
	states := []Entity{
		{
			Type:       "host",
			ID:         map[string]string{"host.id": "0f7a3c1e-aaaa-bbbb-cccc-000000000001"},
			Attributes: map[string]string{"host.name": "fixture-1", "os.type": "linux"},
			Interval:   300 * time.Second,
		},
		{
			Type:     "service.listener",
			ID:       map[string]string{"service.endpoint": "0f7a3c1e-aaaa-bbbb-cccc-000000000001:8080/tcp"},
			Interval: 300 * time.Second,
			Relationships: []Relationship{{
				Type:       "runs_on",
				TargetType: "host",
				TargetID:   map[string]string{"host.id": "0f7a3c1e-aaaa-bbbb-cccc-000000000001"},
			}},
		},
	}
	deletes := []Entity{{
		Type: "service.listener",
		ID:   map[string]string{"service.endpoint": "0f7a3c1e-aaaa-bbbb-cccc-000000000001:9999/tcp"},
	}}
	return c, states, deletes
}

// TestBuildDeterministic pins the byte determinism the fixture relies on:
// identical input produces identical bytes, regardless of map iteration order.
func TestBuildDeterministic(t *testing.T) {
	c, states, _ := fixtureClient(t)
	a, err := c.Build("entity.state", states)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Build("entity.state", states)
	if err != nil {
		t.Fatal(err)
	}
	ab, _ := plogotlp.NewExportRequestFromLogs(a).MarshalProto()
	bb, _ := plogotlp.NewExportRequestFromLogs(b).MarshalProto()
	if !bytes.Equal(ab, bb) {
		t.Fatal("two builds of the same input differ — map ordering leaked into the wire form")
	}
}

// TestFixtureV1 is the published-contract pin: the SDK reproduces the
// checked-in fixture byte for byte. Run with -update to regenerate after a
// DELIBERATE contract change (and say so loudly in the changelog).
func TestFixtureV1(t *testing.T) {
	c, states, _ := fixtureClient(t)
	ld, err := c.Build("entity.state", states)
	if err != nil {
		t.Fatal(err)
	}
	got, err := plogotlp.NewExportRequestFromLogs(ld).MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "fixture_v1.bin")
	if *update {
		if werr := os.WriteFile(path, got, 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture (run with -update once to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("SDK output diverged from the published fixture v1 (%d vs %d bytes) — if the contract change is deliberate, regenerate with -update and changelog it", len(got), len(want))
	}
}

// TestBuildValidation pins the SDK-side guardrails: a producer cannot even
// build a record Toise would reject.
func TestBuildValidation(t *testing.T) {
	c := &Client{opts: Options{}, now: fixedClock}
	if _, err := c.Build("entity.state", []Entity{{ID: map[string]string{"k": "v"}}}); err == nil {
		t.Error("missing Type must fail at build")
	}
	if _, err := c.Build("entity.state", []Entity{{Type: "host"}}); err == nil {
		t.Error("empty ID must fail at build")
	}
	if _, err := c.Build("entity.state", []Entity{{Type: "host", ID: map[string]string{"host.id": "h"},
		Relationships: []Relationship{{Type: "runs_on"}}}}); err == nil {
		t.Error("incomplete relationship must fail at build")
	}
	if _, err := c.Build("entity.bogus", []Entity{{Type: "host", ID: map[string]string{"host.id": "h"}}}); err == nil {
		t.Error("unknown event name must fail at build")
	}
	if _, err := c.Build("entity.state", []Entity{{Type: "host", ID: map[string]string{"host.id": "h"},
		Interval: 500 * time.Millisecond}}); err == nil {
		t.Error("sub-second Interval must fail at build (would round report.interval to 0)")
	}
	// A whole-second interval is fine.
	if _, err := c.Build("entity.state", []Entity{{Type: "host", ID: map[string]string{"host.id": "h"},
		Interval: time.Second}}); err != nil {
		t.Errorf("1s Interval must build: %v", err)
	}
}

// TestSDKOutputIsConformant closes the loop SDK-side: everything the SDK
// builds passes the conformance kit.
func TestSDKOutputIsConformant(t *testing.T) {
	c, states, deletes := fixtureClient(t)
	for name, ents := range map[string][]Entity{"entity.state": states, "entity.delete": deletes} {
		ld, err := c.Build(name, ents)
		if err != nil {
			t.Fatal(err)
		}
		if problems := conformance.Check(ld); len(problems) != 0 {
			t.Fatalf("SDK %s output is non-conformant: %v", name, problems)
		}
	}
}
