package ingest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/toise-dev/toise/pkg/emit/conformance"
)

// TestCheckNeverPassesWhatIngestRejects is the property behind the conformance
// kit's claim: a record that Check passes (no non-advisory problem) is never
// rejected per record by the real receiver. The receiver runs with
// accept_unknown_types so the claim under test is exactly the shape contract;
// vocabulary membership is a separately documented, separately enforced layer.
// Inputs are randomized but seeded, so a failure reproduces.
func TestCheckNeverPassesWhatIngestRejects(t *testing.T) {
	addr := startIngest(t, true)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := plogotlp.NewGRPCClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rng := rand.New(rand.NewPCG(42, 0))
	var passed, rejectedAsExpected int
	for i := 0; i < 400; i++ {
		ld := randomEntityLogs(rng)
		var shapeProblems []string
		for _, p := range conformance.Check(ld) {
			if !p.Advisory {
				shapeProblems = append(shapeProblems, p.String())
			}
		}
		resp, eerr := client.Export(ctx, plogotlp.NewExportRequestFromLogs(ld))
		if eerr != nil {
			t.Fatalf("iteration %d: export failed at the transport: %v\nrecord: %s", i, eerr, dumpLogs(t, ld))
		}
		rejected := resp.PartialSuccess().RejectedLogRecords()
		if len(shapeProblems) == 0 {
			passed++
			if rejected > 0 {
				t.Errorf("iteration %d: Check passed but ingest rejected %d record(s): %s\nrecord: %s",
					i, rejected, resp.PartialSuccess().ErrorMessage(), dumpLogs(t, ld))
			}
		} else if rejected > 0 {
			rejectedAsExpected++
		}
	}
	// The generator must actually exercise both sides of the boundary, or the
	// property is vacuous.
	if passed == 0 || rejectedAsExpected == 0 {
		t.Fatalf("generator imbalance: %d Check-passing and %d Check-failing-and-rejected records", passed, rejectedAsExpected)
	}
}

// randomEntityLogs builds one entity-event record with randomized, sometimes
// deliberately malformed fields: type (missing/empty/known/unknown/mistyped),
// identity (missing/empty/empty key/non-scalar value/sound), description,
// interval, and embedded relationships (complete, incomplete, empty target key).
func randomEntityLogs(rng *rand.Rand) plog.Logs {
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.instance.id", fmt.Sprintf("prop-%d", rng.IntN(3)))
	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	if rng.IntN(10) == 0 {
		lr.SetEventName("entity.delete")
	} else {
		lr.SetEventName("entity.state")
	}
	a := lr.Attributes()

	switch rng.IntN(8) {
	case 0: // missing
	case 1:
		a.PutStr("entity.type", "")
	case 2:
		a.PutInt("entity.type", 7)
	case 3:
		a.PutStr("entity.type", "totally.unknown.type")
	default:
		a.PutStr("entity.type", "host")
	}

	switch rng.IntN(10) {
	case 0: // missing
	case 1:
		a.PutEmptyMap("entity.id")
	case 2:
		a.PutStr("entity.id", "not-a-map")
	case 3:
		a.PutEmptyMap("entity.id").PutStr("", "empty-key")
	case 4:
		m := a.PutEmptyMap("entity.id")
		m.PutStr("host.id", fmt.Sprintf("h-%d", rng.IntN(50)))
		m.PutEmptySlice("nested")
	default:
		a.PutEmptyMap("entity.id").PutStr("host.id", fmt.Sprintf("h-%d", rng.IntN(50)))
	}

	switch rng.IntN(6) {
	case 0:
		a.PutEmptyMap("entity.description").PutStr("", "empty-key")
	case 1:
		a.PutEmptyMap("entity.description").PutEmptyMap("nested")
	case 2, 3:
		a.PutEmptyMap("entity.description").PutStr("host.name", "prop")
	default: // none
	}

	switch rng.IntN(5) {
	case 0:
		a.PutInt("entity.report.interval", 300)
	case 1:
		a.PutStr("entity.report.interval", "300")
	case 2:
		a.PutInt("entity.report.interval", -1)
	default: // none
	}

	if rng.IntN(3) == 0 {
		sl := a.PutEmptySlice("entity.relationships")
		switch rng.IntN(4) {
		case 0: // incomplete descriptor
			sl.AppendEmpty().SetEmptyMap().PutStr("relationship.type", "runs_on")
		case 1: // empty key in the target identity
			m := sl.AppendEmpty().SetEmptyMap()
			m.PutStr("relationship.type", "runs_on")
			m.PutStr("entity.type", "host")
			m.PutEmptyMap("entity.id").PutStr("", "empty-key")
		case 2: // non-map descriptor
			sl.AppendEmpty().SetStr("not-a-descriptor")
		default: // complete
			m := sl.AppendEmpty().SetEmptyMap()
			m.PutStr("relationship.type", "runs_on")
			m.PutStr("entity.type", "host")
			m.PutEmptyMap("entity.id").PutStr("host.id", fmt.Sprintf("h-%d", rng.IntN(50)))
		}
	}
	return ld
}

func dumpLogs(t *testing.T, ld plog.Logs) string {
	t.Helper()
	raw, err := (&plog.JSONMarshaler{}).MarshalLogs(ld)
	if err != nil {
		return fmt.Sprintf("<marshal failed: %v>", err)
	}
	return string(raw)
}
