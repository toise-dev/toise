package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"

	// Register the gzip decompressor so the server can accept gzip-encoded
	// OTLP exports. gRPC-Go does not install it by default, yet gzip is the
	// OTel ecosystem default (the OTel SDK and senhub-agent compress with it):
	// without this, gzip'd exports fail at the transport with "Decompressor is
	// not installed" before reaching the handler — a silent drop on the wire.
	_ "google.golang.org/grpc/encoding/gzip"

	"github.com/toise-dev/toise/internal/change"
)

// Receiver is an OTLP/gRPC logs server that routes entity events to the change
// engine.
type Receiver struct {
	srv    *grpc.Server
	logs   *logsServer
	logger *slog.Logger
}

// NewReceiver builds a receiver routing to e. A nil logger uses slog.Default.
func NewReceiver(e *change.Engine, logger *slog.Logger) *Receiver {
	if logger == nil {
		logger = slog.Default()
	}
	srv := grpc.NewServer()
	ls := &logsServer{engine: e, logger: logger, embedded: newEmbeddedReconciler()}
	plogotlp.RegisterGRPCServer(srv, ls)
	return &Receiver{srv: srv, logs: ls, logger: logger}
}

// Serve accepts connections on lis until Stop is called. It blocks.
func (r *Receiver) Serve(lis net.Listener) error {
	r.logger.Info("otlp receiver listening", "addr", lis.Addr().String())
	if err := r.srv.Serve(lis); err != nil {
		return fmt.Errorf("serving otlp: %w", err)
	}
	return nil
}

// Stop gracefully stops the server.
func (r *Receiver) Stop() { r.srv.GracefulStop() }

// logsServer implements the OTLP logs service.
type logsServer struct {
	plogotlp.UnimplementedGRPCServer
	engine   *change.Engine
	logger   *slog.Logger
	embedded *embeddedReconciler
}

// Export ingests a batch of OTLP logs, routing entity-event LogRecords to the
// change engine and ignoring the rest.
func (s *logsServer) Export(_ context.Context, req plogotlp.ExportRequest) (plogotlp.ExportResponse, error) {
	ld := req.Logs()
	var handled, skipped int
	var dropped []string

	// Each ResourceLogs (one producer) is ingested as a single batch so its events
	// commit with one durable append (one fsync) instead of one per record.
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		producer, _ := strAttr(rls.At(i).Resource().Attributes(), resAttrProducer)
		sls := rls.At(i).ScopeLogs()
		var routeErr error
		batchErr := s.engine.Batch(func(b *change.Batch) {
			for j := 0; j < sls.Len() && routeErr == nil; j++ {
				recs := sls.At(j).LogRecords()
				for k := 0; k < recs.Len(); k++ {
					lr := recs.At(k)
					ok, drop, err := routeRecord(b, lr, producer)
					dropped = append(dropped, drop...)
					if err != nil {
						routeErr = err
						break
					}
					// Embedded relationships ride on entity-state events (spec PR
					// #4836); reconcile them additively alongside routeRecord.
					edrop, eerr := s.embedded.handle(b, lr)
					dropped = append(dropped, edrop...)
					if eerr != nil {
						routeErr = eerr
						break
					}
					if ok {
						handled++
					} else {
						skipped++
					}
				}
			}
		})
		// A routing or append failure is retriable (at-least-once + idempotent
		// classification make a retry safe): surface it.
		if routeErr != nil {
			return plogotlp.NewExportResponse(), fmt.Errorf("routing log record: %w", routeErr)
		}
		if batchErr != nil {
			return plogotlp.NewExportResponse(), fmt.Errorf("ingest batch: %w", batchErr)
		}
	}
	if len(dropped) > 0 {
		// Non-scalar attribute values are dropped (producers must send flat scalar
		// maps) — surface it, never lose data silently.
		s.logger.Warn("dropped non-scalar attribute values at ingest boundary", "keys", dropped)
	}
	if skipped > 0 {
		s.logger.Debug("otlp export processed", "entity_events", handled, "ignored", skipped)
	}
	return plogotlp.NewExportResponse(), nil
}
