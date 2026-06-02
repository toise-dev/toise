# Conformance fixture — producer↔consumer contract

`entity-events.json` is a small batch of **OTLP/JSON `LogRecord`s** that encodes
the agreed entity-event contract between Toise (consumer) and its producers
(first: senhub-agent, [#185](https://github.com/senhub-io/senhub-agent/issues/185)).
It is the **shared, executable interface** between the two sides:

- **Toise** ingests it in `internal/ingest` (`TestConformanceFixture`) and asserts
  the resulting graph. A change on the Toise side that breaks the contract fails
  that test.
- **A producer** (senhub-agent) emits entity events and replays this batch / asserts
  it can reproduce the same shape, so a producer-side drift is caught in its CI.

It exercises the full contract: the standard `otel.entity.*` node shape, the
`entity.relation.*` edge extension (strict purity — relation records carry no
`otel.entity.*`, discriminated by `entity.relation.event.type`), flat scalar maps,
exact-identity endpoints emitted before their edges, an attribute update, an
explicit `entity_delete`, a `relation_delete`, and the producer vocabulary
(`host`, `service.instance`, `db` with a stable source identity key,
`network.device`; relations `runs_on` (agent→host), `monitors` (agent→db),
`adjacent_to`).

The file is **generated** from `buildConformanceLogs` — do not hand-edit it.
Regenerate after an intentional contract change:

```bash
go test ./internal/ingest -run TestConformanceFixture -update-conformance
```

The wire contract this fixture embodies is documented in
[`docs/data-model/otel-mapping.md`](../../../../docs/data-model/otel-mapping.md)
and [`senhub-agent-contract.md`](../../../../docs/data-model/senhub-agent-contract.md).
