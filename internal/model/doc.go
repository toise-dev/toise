// Package model defines Toise's domain types: entities, relations, the typed
// attribute Value, and the bi-temporal, classified events that flow through the
// system.
//
// These are hand-written Go types, ergonomic and decoupled from any wire
// format. The durable/interchange contract is the Protocol Buffers definition
// in proto/toise/v1; this package converts to and from it via ToProto/FromProto
// helpers. See ADR 0004 (data model), ADR 0005 (bi-temporality), ADR 0006
// (change taxonomy) and ADR 0017 (entity identity).
//
// Identity. An entity has a stable logical ID (a ULID, assigned on first sight
// and stable across identity changes) and an identity hash (a deterministic
// fingerprint of its current identifying attributes). The logical ID is what
// consumers reference; the hash powers idempotent ingest and lookup.
package model
