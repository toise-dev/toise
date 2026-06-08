package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"

	// Register the gzip decompressor so the server can accept gzip-encoded
	// OTLP exports. gRPC-Go does not install it by default, yet gzip is the
	// OTel ecosystem default (the OTel SDK and senhub-agent compress with it):
	// without this, gzip'd exports fail at the transport with "Decompressor is
	// not installed" before reaching the handler — a silent drop on the wire.
	_ "google.golang.org/grpc/encoding/gzip"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/tenant"
)

// Receiver is an OTLP/gRPC logs server that routes entity events to the change
// engine.
type Receiver struct {
	srv    *grpc.Server
	logs   *logsServer
	logger *slog.Logger
}

// NewReceiver builds a receiver routing every record to e (single tenant). A nil
// logger uses slog.Default. It is shorthand for NewRoutedReceiver with a constant
// engine.
func NewReceiver(e *change.Engine, logger *slog.Logger, opts ...grpc.ServerOption) *Receiver {
	return NewRoutedReceiver(func(string) (*change.Engine, error) { return e, nil }, logger, opts...)
}

// NewRoutedReceiver builds a receiver that resolves the change engine per tenant.
// engineFor maps a (sanitized) tenant id to its engine; the tenant is read from
// the X-Scope-OrgID gRPC metadata and may be overridden per ResourceLogs by a
// tenant.id resource attribute (ADR 0025, #95). A nil logger uses slog.Default.
func NewRoutedReceiver(engineFor func(tenantID string) (*change.Engine, error), logger *slog.Logger, opts ...grpc.ServerOption) *Receiver {
	if logger == nil {
		logger = slog.Default()
	}
	srv := grpc.NewServer(opts...)
	ls := &logsServer{engineFor: engineFor, logger: logger, reconcilers: make(map[string]*embeddedReconciler)}
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
	engineFor func(tenantID string) (*change.Engine, error)
	logger    *slog.Logger

	// reconcilers holds the embedded-relationship state per tenant. It must be
	// per-tenant: two tenants may assert the same source entity key, and a shared
	// reconciler would let one tenant's re-emit remove the other's relations.
	mu          sync.Mutex
	reconcilers map[string]*embeddedReconciler
}

func (s *logsServer) reconcilerFor(tenantID string) *embeddedReconciler {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reconcilers[tenantID]
	if !ok {
		r = newEmbeddedReconciler()
		s.reconcilers[tenantID] = r
	}
	return r
}

// Export ingests a batch of OTLP logs, routing entity-event LogRecords to the
// per-tenant change engine and ignoring the rest. The tenant comes from the
// request's X-Scope-OrgID metadata and may be overridden per ResourceLogs by a
// tenant.id resource attribute (so one OTLP stream can carry several tenants).
func (s *logsServer) Export(ctx context.Context, req plogotlp.ExportRequest) (plogotlp.ExportResponse, error) {
	ld := req.Logs()
	var handled, skipped int
	var dropped []string

	// An invalid X-Scope-OrgID is rejected rather than silently folded into the
	// default tenant — a tenant id that cannot be honored is a caller error.
	baseTenant, ok := tenant.FromGRPC(ctx)
	if !ok {
		return plogotlp.NewExportResponse(), fmt.Errorf("invalid %s metadata", tenant.HeaderOrgID)
	}

	// Each ResourceLogs (one producer) is ingested as a single batch so its events
	// commit with one durable append (one fsync) instead of one per record.
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		res := rls.At(i).Resource()
		producer, _ := strAttr(res.Attributes(), resAttrProducer)
		tenantID := baseTenant
		if rt, ok := strAttr(res.Attributes(), tenant.ResourceAttr); ok {
			san, ok := tenant.Sanitize(rt)
			if !ok {
				return plogotlp.NewExportResponse(), fmt.Errorf("invalid %s resource attribute %q", tenant.ResourceAttr, rt)
			}
			tenantID = san
		}
		engine, err := s.engineFor(tenantID)
		if err != nil {
			return plogotlp.NewExportResponse(), fmt.Errorf("resolving tenant %q: %w", tenantID, err)
		}
		reconciler := s.reconcilerFor(tenantID)
		sls := rls.At(i).ScopeLogs()
		var routeErr error
		batchErr := engine.Batch(func(b *change.Batch) {
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
					edrop, eerr := reconciler.handle(b, lr)
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
