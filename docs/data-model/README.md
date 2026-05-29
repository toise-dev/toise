# Data Model

This document introduces the Toise data model. It is a living overview; the
formal, authoritative contract will be defined in Protocol Buffers under
[`proto/toise/v1/`](../../proto/toise/v1/) as the model stabilizes.

## Alignment with OpenTelemetry

Toise is OpenTelemetry-native. The data model aligns with the
[OpenTelemetry Entity data model](https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/entities/data-model.md)
and reuses [semantic conventions](https://opentelemetry.io/docs/specs/semconv/)
wherever an entity or attribute already has a standard definition. The goal is
that Toise entities slot naturally into an existing OTel deployment rather than
introducing a parallel vocabulary.

In OTel terms, Toise tracks **entities** (things that exist), the
**attributes** that describe them, and the **relationships** that connect them.
Toise adds the dimension OTel signals (metrics, logs, traces) do not carry on
their own: the live topology and inventory of the infrastructure those signals
come from.

## Planned entities

The first entities Toise intends to model:

| Entity            | Description                                                        |
| ----------------- | ------------------------------------------------------------------ |
| `Device`          | A physical or virtual network device (router, switch, firewall).   |
| `NetworkInstance` | A routing/forwarding instance on a device (e.g. a VRF).            |
| `Interface`       | A network interface on a device.                                   |
| `IPAddress`       | An IP address, typically bound to an interface.                    |
| `Prefix`          | An IP prefix / subnet.                                              |
| `NextHop`         | A next-hop entry in a forwarding or routing decision.              |
| `BGPPeer`         | A BGP peering session endpoint.                                    |
| `ASN`             | An autonomous system number.                                       |
| `Link`            | A connection between two interfaces (layer 1/2 adjacency).        |
| `Host`            | A compute host (physical server, VM, hypervisor guest).           |
| `Service`         | A service running on a host or set of hosts.                       |

## Planned relationships

Relationships are first-class. Early examples include:

- `Interface` **belongs to** `Device`
- `IPAddress` **assigned to** `Interface`
- `IPAddress` **member of** `Prefix`
- `Link` **connects** `Interface` ↔ `Interface`
- `BGPPeer` **peers with** `BGPPeer`, each **advertises** `Prefix`
- `BGPPeer` **belongs to** `ASN`
- `NextHop` **resolves to** `IPAddress` / `Interface`
- `Service` **runs on** `Host`
- `Host` **connects through** `Interface`

This list will grow and be refined as receivers are implemented and as the
event model (see [ADR-0002](../architecture/adr/0002-event-sourcing-as-storage-pattern.md))
takes shape.

## Status

Everything here is provisional and subject to change. Nothing in this document
is a stable contract yet. Follow the ADR log and the `proto/toise/v1/`
definitions for the authoritative state.
