package emit

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
)

// stubServer is an OTLP logs server that accepts every export and, when
// configured, reports rejected records via OTLP partial success — the path a
// real Toise takes for per-record contract violations.
type stubServer struct {
	plogotlp.UnimplementedGRPCServer
	rejected int64
	message  string
}

func (s *stubServer) Export(context.Context, plogotlp.ExportRequest) (plogotlp.ExportResponse, error) {
	resp := plogotlp.NewExportResponse()
	if s.rejected > 0 {
		ps := resp.PartialSuccess()
		ps.SetRejectedLogRecords(s.rejected)
		ps.SetErrorMessage(s.message)
	}
	return resp, nil
}

func startStub(t *testing.T, s *stubServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	plogotlp.RegisterGRPCServer(srv, s)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func dialStub(t *testing.T, addr string) *Client {
	t.Helper()
	c, err := New(Options{Endpoint: addr, ServiceName: "partial-producer", ServiceInstanceID: "partial-01"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestPartialSuccessSurfacesAsPartialError pins that a server-side partial
// success is not swallowed: State and Delete return a PartialError carrying
// the rejected count and the server's message, while a fully-accepted export
// still returns nil.
func TestPartialSuccessSurfacesAsPartialError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ents := []Entity{
		{Type: "host", ID: map[string]string{"host.id": "h-1"}},
		{Type: "host", ID: map[string]string{"host.id": "h-2"}},
	}

	rejecting := dialStub(t, startStub(t, &stubServer{rejected: 1, message: "invalid entity record: unknown entity type"}))
	for name, do := range map[string]func() error{
		"State":  func() error { return rejecting.State(ctx, ents...) },
		"Delete": func() error { return rejecting.Delete(ctx, ents...) },
	} {
		err := do()
		var pe PartialError
		if !errors.As(err, &pe) {
			t.Fatalf("%s = %v, want a PartialError", name, err)
		}
		if pe.Rejected != 1 {
			t.Errorf("%s: Rejected = %d, want 1", name, pe.Rejected)
		}
		if !strings.Contains(pe.Message, "unknown entity type") {
			t.Errorf("%s: Message = %q, want the server's rejection reason", name, pe.Message)
		}
		if !strings.Contains(pe.Error(), "rejected 1 record") {
			t.Errorf("%s: Error() = %q, want the count in the text", name, pe.Error())
		}
	}

	accepting := dialStub(t, startStub(t, &stubServer{}))
	if err := accepting.State(ctx, ents...); err != nil {
		t.Fatalf("fully-accepted State = %v, want nil", err)
	}
	if err := accepting.Delete(ctx, ents...); err != nil {
		t.Fatalf("fully-accepted Delete = %v, want nil", err)
	}
}
