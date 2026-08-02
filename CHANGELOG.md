# Changelog

All notable changes to platformctl are recorded here.

Semantic versioning. The project is pre-1.0 and pre-implementation, so breaking
schema changes land in MINOR releases rather than MAJOR ones.

## [0.3.0] - 2026-08-03

### Added

- `go.mod` — module `platform.ryxen.dev/platformctl`, Go 1.24. Nothing can live
  under `internal/` without it.
- `internal/codes` — the diagnostic code registry, now the single source of
  truth as CLAUDE.md requires. 67 preflight codes collected from
  `docs/10-preflight-plan.md` and `docs/20-cert.md` §4, each carrying family,
  category, English summary, default message, severity and failure sub-codes.
- `PF-612` "Gateway external address is pinned".
- Collision, inventory and dangling-reference tests. The last one walks the
  repository and fails on any code cited in Go or YAML that the registry does
  not define; it is what would have caught `PF-908` at the time it was written.

### Changed

- `PF-908` is retired and its meaning now lives at `PF-612`. `api/v1alpha1`
  referenced `PF-908` although no document ever defined it, and `PF-9xx` is
  reserved by `docs/20-cert.md` for certificate material — an addressing and
  DNS lead-time check does not belong in that block. The number is not reused.

### Fixed

- `types.go` cited `PF-403/404` for the Longhorn prerequisites. `PF-403` is
  inode headroom; the Longhorn checks are `PF-404` (iscsid), `PF-405` (NFS
  client) and `PF-406` (multipathd).

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
