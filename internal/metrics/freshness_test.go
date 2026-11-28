package metrics

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestProducerLastSeenCollector(t *testing.T) {
	src := func() []ProducerSeen {
		return []ProducerSeen{
			{
				Tenant:   "tenant-a",
				Producer: "11111111-2222-4333-8444-555555555555",
				Scope:    "test/method-a",
				LastSeen: time.Unix(1700000000, 0),
			},
		}
	}
	want := `# HELP toise_producer_last_seen_timestamp_seconds Unix time at which a producer last spoke, by tenant, producer and collection method (scope is empty when unknown). Measured on this replica's clock at observation, so it is absent for a replica that only receives the shipped log. A silent producer is not necessarily down. Series leave when the speaker's last liveness reference is swept.
# TYPE toise_producer_last_seen_timestamp_seconds gauge
toise_producer_last_seen_timestamp_seconds{producer="11111111-2222-4333-8444-555555555555",scope="test/method-a",tenant="tenant-a"} 1.7e+09
`
	if err := testutil.CollectAndCompare(
		NewProducerLastSeen(src),
		strings.NewReader(want),
		"toise_producer_last_seen_timestamp_seconds",
	); err != nil {
		t.Errorf("exposition mismatch: %v", err)
	}
}

func TestProducerLastSeenScopedPerTenant(t *testing.T) {
	src := func() []ProducerSeen {
		return []ProducerSeen{
			{
				Tenant:   "tenant-a",
				Producer: "11111111-2222-4333-8444-555555555555",
				Scope:    "test/method-a",
				LastSeen: time.Unix(1700000000, 0),
			},
			{
				Tenant:   "tenant-b",
				Producer: "99999999-2222-4333-8444-555555555555",
				Scope:    "test/method-a",
				LastSeen: time.Unix(1700000001, 0),
			},
		}
	}

	h := ScopedHandler(
		func(*http.Request) (string, bool) { return "tenant-a", false },
		NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123"),
		NewProducerLastSeen(src),
	)
	body := scrape(t, h)
	if !strings.Contains(body, `toise_producer_last_seen_timestamp_seconds{producer="11111111-2222-4333-8444-555555555555"`) {
		t.Fatalf("expected tenant-a producer series, got:\n%s", body)
	}
	if strings.Contains(body, "99999999-2222-4333-8444-555555555555") {
		t.Fatalf("tenant-b producer series leaked into scoped output:\n%s", body)
	}

	hAll := ScopedHandler(
		func(*http.Request) (string, bool) { return "", true },
		NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123"),
		NewProducerLastSeen(src),
	)
	bodyAll := scrape(t, hAll)
	if !strings.Contains(bodyAll, "11111111-2222-4333-8444-555555555555") {
		t.Fatalf("expected tenant-a producer series in all-tenants output")
	}
	if !strings.Contains(bodyAll, "99999999-2222-4333-8444-555555555555") {
		t.Fatalf("expected tenant-b producer series in all-tenants output")
	}
}
