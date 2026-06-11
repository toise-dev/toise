package ingest

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/pkg/emit"
)

// startIngest runs a real receiver over a fresh store/engine and returns its
// address. acceptUnknown opens the vocabulary on both the receiver and the
// store, matching how the server wires accept_unknown_types end to end.
func startIngest(t *testing.T, acceptUnknown bool) string {
	t.Helper()
	cfg := store.DefaultConfig()
	cfg.AcceptUnknownTypes = acceptUnknown
	st, err := store.Open(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := change.New(projection.New(), st, change.WithRelationBuffer(30*time.Second))
	rec := NewRoutedReceiver(func(string) (*change.Engine, error) { return eng, nil }, nil, nil, acceptUnknown, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = rec.Serve(lis) }()
	t.Cleanup(rec.Stop)
	return lis.Addr().String()
}

// TestSDKSurfacesPartialSuccess pins the producer-visible half of per-record
// rejection: against a real strict-vocabulary receiver, an export mixing a
// valid record with a contract violation succeeds at the transport but must
// come back to the SDK caller as a typed PartialError — not as a silent nil.
func TestSDKSurfacesPartialSuccess(t *testing.T) {
	addr := startIngest(t, false)
	client, err := emit.New(emit.Options{Endpoint: addr, ServiceName: "partial-producer", ServiceInstanceID: "partial-01"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = client.State(ctx,
		emit.Entity{Type: "host", ID: map[string]string{"host.id": "ps-1"}},
		emit.Entity{Type: "definitely.not.registered", ID: map[string]string{"k": "v"}},
	)
	var pe emit.PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("State with one invalid record = %v, want a PartialError", err)
	}
	if pe.Rejected != 1 {
		t.Errorf("Rejected = %d, want 1", pe.Rejected)
	}
	if !strings.Contains(pe.Message, "unknown") {
		t.Errorf("Message = %q, want the receiver's rejection reason", pe.Message)
	}

	if serr := client.State(ctx, emit.Entity{Type: "host", ID: map[string]string{"host.id": "ps-2"}}); serr != nil {
		t.Fatalf("fully-valid State = %v, want nil", serr)
	}
}
