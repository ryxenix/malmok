# Changelog

All notable changes to platformctl are recorded here.

Semantic versioning. The project is pre-1.0 and pre-implementation, so breaking
schema changes land in MINOR releases rather than MAJOR ones.

## [0.9.0] - 2026-08-03

### Added

- `cmd/platformctl` — the first runnable binary, with `attach` as its only
  subcommand. Commands are registered as they are implemented; one that exists
  and does nothing is worse at a customer site than one that is absent.
- `platformctl attach` resolves the event file from `--file`, else the newest
  run under `--bundle`, else the bundle's shared event log. `--follow=false`
  replays a finished run, `--verbose` includes log lines, and `-o json` passes
  the stream through unchanged — the engine already emits JSONL, so that format
  is the stream itself rather than a second serialisation.
- `github.com/spf13/cobra`, the CLI framework fixed by `CLAUDE.md`.

### Changed

- The failure summary lists only originating failures — probe and step events.
  A failed phase or run restates the step that failed, and counting all three
  turned one incident into three entries.

## [0.8.0] - 2026-08-03

### Added

- `internal/attach`: replay an event file, then tail it. Implements
  `docs/11-execute.md` §1.2 and §5.6. It waits for the file to appear, survives
  rotation, never consumes a partial trailing line, and filters to one run when
  a fixed `output.eventLog` holds several.
- `TextRenderer`, the first consumer of the stream, plus `Collector` for tests
  and `--output json`. The progress bar is drawn here — `Bar()` is the only
  place block glyphs exist, and a test asserts that an event carrying its output
  is rejected by `Event.Validate`. That is §5.3 made mechanical instead of
  aspirational.
- `GapReporter`, an optional `Sink` extension for gaps that survive a re-read.
- `LatestRun` and `LatestRunDir` for `attach` with no arguments. Run ids are
  ULIDs, so lexical order is chronological and no clock has to be trusted.

### Changed

- `docs/11-execute.md` §5.6: a gap now triggers at most one re-read. Re-reading
  recovers lines this reader missed but cannot conjure lines the file lacks, so
  a gap that survives is reported and accepted. Unconditional re-reading put the
  follower in a loop it never left — the test caught it.

## [0.7.0] - 2026-08-03

### Added

- `internal/state`, implementing `docs/11-execute.md` §4: the run state file,
  the resume decision, and atomic persistence. Every §7 group B acceptance
  criterion is covered by a test.
- `State.Decide` returns run / skip / **confirm**. The third exists because a
  `oneShot` step left running or failed is neither obviously safe nor obviously
  unsafe to repeat, and deciding automatically goes wrong in one direction or
  the other.
- `Save` writes through a temporary file in the same directory with `fsync`,
  `rename`, then a directory `fsync`. This closes the open question in
  §11-execute about atomic state updates.
- `CheckResumable` refuses a run whose `cluster.yaml` digest has changed, and
  names `--force-resume` in the error rather than just failing.

### Changed

- `docs/11-execute.md` §4.1's state file example now matches the
  implementation: node progress is recorded per step as `step@node` rather than
  as a `nodes` summary, which is the granularity criterion B4 needs. The node
  rollup is derived instead of stored so it cannot drift from the steps it
  summarises. §4.2 gains the three-way resume decision table.
- Step keys split on the first `@`, putting the "no `@`" constraint on step ids
  — constants we author — rather than on node ids, which come from a customer's
  `cluster.yaml` and are whatever they are.

## [0.6.0] - 2026-08-03

### Added

- `internal/event`, the first implementation of the `docs/11-execute.md` §5
  contract: the `Event` schema, a sequence-owning append-only `Writer`, a
  `Scanner` that verifies per-run sequence continuity, and replay helpers for
  `attach`.
- `Event.Validate` enforces the §7 group C acceptance criteria in the engine
  itself rather than only in tests — codes must exist in `internal/codes`, a
  failed or blocked status must carry one, detail must be single-line printable
  ASCII, and no event may contain ANSI escapes or block-drawing glyphs.
- `TestScreenElementsFromADR002` encodes criterion C6: every screen element in
  the ADR-002 mockup is constructed as an event and validated, so a schema too
  thin to draw the TUI fails the build instead of being discovered when the TUI
  reaches into the engine for a missing value.
- `Timestamp` renders RFC3339 at exactly millisecond precision. `time.Time`'s
  variable-width nanoseconds make event files noisy to diff and to read on site.

### Changed

- `internal/codes`' dangling-reference test now skips `_test.go`. Negative tests
  must name unregistered codes to prove the rejection path; the guard exists for
  production code, which is where `PF-908` actually lived.

## [0.5.0] - 2026-08-03

### Added

- `docs/11-execute.md`, the engine↔renderer contract and the last unwritten WP
  document: 13-phase catalogue with grades and ordering constraints, the
  step-level idempotency contract (`Observe`/`Satisfied`/`Apply`), the resume
  model and state file, the JSONL event schema, retry policy, and §7 acceptance
  criteria in five groups.
- `platformctl attach` and `apply --detach` in `docs/00-architecture.md` §4.
  The engine outliving the renderer needs a way back in, and a dropped SSH
  session at a customer site is routine rather than exceptional.

### Changed

- `CLAUDE.md`'s prohibition table said the TUI is "merely a cluster.yaml
  generator". That reading is what would make someone remove the TUI's execute
  button, and implementation sessions read `CLAUDE.md` rather than the ADRs, so
  the row is rewritten and followed by an explicit note: installs run inside the
  TUI; the only forbidden thing is the TUI owning installation logic.
- `api/v1alpha1/types.go` no longer claims the TUI is "never a participant in
  execution". The real invariant — no code path may *require* a terminal — is
  stated separately from who initiates the run.

### Fixed

- `docs/00-architecture.md` §1.1 and §1.2 reverted to `preflight-probes.md`,
  `cert-bundle.md`, `day2-maintenance.md` and `ingress.go` when the ADR-002
  rewrite landed on a pre-`b6ea94a` copy. Restored, with `11-execute.md` added.

## [0.4.2] - 2026-08-03

### Added

- `docs/30-maintenance.md` §2.5, separating the three certificate expiry
  thresholds into named roles: ① apply gate (per bundle, drives `PF-904`),
  ② alert firing (per certificate series, cluster-wide), ③ report grading (per
  contract). It also records why collapsing them into one number breaks
  something either way — a 45-day report threshold lets a quarterly report ship
  past an expiry, and a 90-day alert fires daily for three months until nobody
  reads it. Roles ② and ③ still have no `ClusterSpec` fields; §2.5 marks that
  explicitly, and `maintenance.thresholds` in §4.3 remains documentation rather
  than schema.
- Cross-references to §2.5 from `docs/20-cert.md` (`PF-904`),
  `api/v1alpha1/gateway.go`, `internal/codes/preflight.go` and the DMZ example.

### Changed

- `BYOMaterial.ExpiryWarningDays` default 30 → 45, matching the series A first
  alert step in §2.4. The value had disagreed with both documents since v0.1.
- §2.4 and §4.3 headings now name which role they define.

## [0.4.1] - 2026-08-03

### Fixed

- `docs/00-architecture.md` §1.1 and §1.2 named `preflight-probes.md`,
  `cert-bundle.md` and `day2-maintenance.md`, which had been renamed to the
  numbered scheme before v0.1 was packaged. §1.1 also gains `internal/codes`
  and `99-codes.md`, the two sources of truth it was missing.
- `ProfileCustom` sat inside the const block labelled "Tier-1 profiles" while
  being Tier-3 by its own definition. Moved out, so that "every constant in
  that block has a CI lane" — which the release gate depends on — is true.
- `CLAUDE.md` pointed the JSONL event schema at `docs/90-events.md` while its
  own routing table and the README pointed at `docs/11-execute.md`. Both
  documents were unwritten, so the split was settled by decision:
  `docs/11-execute.md` owns the event schema and `90-events.md` will not exist.

### Changed

- `gofmt` applied to `api/v1alpha1/types.go`: whitespace only, seven
  pre-existing misalignments. The repository is now `gofmt` clean, which
  matters as of v0.3.0 when `go.mod` landed.

## [0.4.0] - 2026-08-03

### Added

- `internal/codes` completed: 8 `PV` post-apply verification codes from
  `docs/20-cert.md` §6.5, 42 `MC` maintenance checks from
  `docs/30-maintenance.md` §3, and 5 `DG` downgrade reasons. 122 codes in total.
- `internal/codes/gen` and `go generate ./internal/codes/`, rendering
  `docs/99-codes.md`. The document is an artifact and carries a do-not-edit
  banner; a test diffs it against the registry so it cannot rot unnoticed.
- Inventory tests pinning every family's exact code set, plus determinism and
  banner tests for the generator.

### Changed

- `CLAUDE.md` now names the generated registry path and states that retired
  numbers are never reused.

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
