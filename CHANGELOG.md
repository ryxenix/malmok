# Changelog

All notable changes to platformctl are recorded here.

Semantic versioning. The project is pre-1.0 and pre-implementation, so breaking
schema changes land in MINOR releases rather than MAJOR ones.

## [0.2.0] - 2026-08-03

### Added

- `ClusterSpec.Gateway`. The gateway layer had no entry point in the root schema
  at all, so `examples/cluster-dmz.yaml` could not be decoded.

### Changed

- **BREAKING** `cluster.yaml`: the top-level `ingress` key is now `gateway`.
  No released consumers exist — the engine is unimplemented.
- `IngressSpec` renamed to `GatewaySpec`, and `api/v1alpha1/ingress.go` renamed
  to `api/v1alpha1/gateway.go`. The type modelled the Gateway API throughout —
  GatewayClass, Gateway, listeners, TLS, DNS — and never held a Kubernetes
  Ingress field. Ingress itself is excluded outright by ADR-005.
- `GatewaySpec` (the per-gateway list element) renamed to `Gateway`, freeing the
  name for the type above: `Gateways []Gateway`.
- Default contract ConfigMap renamed from `platform-ingress-contract` to
  `platform-gateway-contract`. Nothing consumes it yet.

### Fixed

- `api/v1alpha1` did not compile. `ListenerTLS` was declared twice — once as a
  `ListenerProtocol` constant and once as the listener TLS config struct. The
  constant is now `ListenerTLSPassthrough`; its wire value stays `"TLS"`, so
  `protocol: TLS` in `cluster.yaml` is unaffected.

## [0.1.0] - 2026-08-02

### Added

- Initial design-stage import. No executable code.
- `CLAUDE.md` — session contract: prohibitions, fixed stack, architectural
  invariants, document routing.
- `docs/00-architecture.md` — 10 ADRs, L0–L3 layer split, Tier scheme with six
  T1 profiles, roadmap.
- `docs/10-preflight-plan.md` — probe catalog `PF-1xx`–`PF-8xx`, dataplane
  downgrade decision tree, `DG-xxx` codes.
- `docs/20-cert.md` — certificate classification and chain assembly, `PF-9xx`
  file gates, `PV-xxx` wire verification.
- `docs/30-maintenance.md` — certificate series A–D, `MC-xxx` check catalog,
  maintenance report structure.
- `api/v1alpha1` — `ClusterSpec` and `IngressSpec`.
- `examples/cluster-dmz.yaml` — DMZ site with mixed per-listener TLS sources.
