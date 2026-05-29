package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"

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
	ls := &logsServer{engine: e, logger: logger}
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
	engine *change.Engine
	logger *slog.Logger
}

// Export ingests a batch of OTLP logs, routing entity-event LogRecords to the
// change engine and ignoring the rest.
func (s *logsServer) Export(_ context.Context, req plogotlp.ExportRequest) (plogotlp.ExportResponse, error) {
	ld := req.Logs()
	var handled, skipped int

	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				ok, err := routeRecord(s.engine, recs.At(k))
				if err != nil {
					// A routing failure (e.g. store append) is retriable: surface it.
					return plogotlp.NewExportResponse(), fmt.Errorf("routing log record: %w", err)
				}
				if ok {
					handled++
				} else {
					skipped++
				}
			}
		}
	}
	if skipped > 0 {
		s.logger.Debug("otlp export processed", "entity_events", handled, "ignored", skipped)
	}
	return plogotlp.NewExportResponse(), nil
}
