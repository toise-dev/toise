# Toise

> The living map of your infrastructure.

Toise is an open-source backend that maintains a live, queryable graph of an
organization's infrastructure — devices, hosts, services, network links,
routing tables, dependencies — by ingesting data from the systems that already
hold the truth (SNMP, gNMI, vSphere, Active Directory, host agents, cloud APIs).

Toise is OpenTelemetry-native and complements modern OTel stacks
(VictoriaMetrics, Grafana, Loki, Tempo) by adding the missing dimension: the
live topology and inventory of the underlying infrastructure.

## Why Toise

Modern observability stacks have closed the visibility gap for applications,
hosts, containers, and services. The network and the living inventory of how
everything connects remain a recurring blind spot. Toise fills it, in the same
paradigm.

## Status

Toise is in early development. Core architecture, data model, and first
receivers are under active design. We are not yet ready for production use.
Expect breaking changes.

## Documentation

Design notes, architecture decisions, and roadmap live in the [`docs/`](./docs)
directory. The public website is at [toise.dev](https://toise.dev).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) and
[CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md).

## License

Apache License 2.0. See [LICENSE](./LICENSE).

## Maintainers

Toise is initiated and primarily maintained by
[Sensor Factory](https://sensorfactory.fr). Contributions from the broader
community are welcome.
