// Package ingest accepts OpenTelemetry entity events over OTLP/gRPC and routes
// them to the change-detection engine (see ADR 0009).
//
// It implements the OTLP logs service using collector pdata types, filters
// LogRecords for entity events (by the LogRecord EventName entity.state /
// entity.delete), converts them into change-engine observations, and hands them
// off. Non-entity LogRecords are ignored. Toise implements no collectors of its
// own; producers
// (a synthetic OTel client in tests; senhub-agent or an OTel Collector in
// production) push to this receiver.
package ingest
