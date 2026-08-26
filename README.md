# Malmok (말목)

One static binary that **builds, grows and upgrades RKE2 clusters**. A TUI
wizard and a CLI over the same engine, with nothing to install on the nodes.

*[한국어 README](README.ko.md)*

> **Status: alpha.** Single-node and two-node clusters are verified repeatedly
> on real hardware, and the tool has built a production cluster in a data
> centre. **Three-server HA, airgapped installs and external registries are
> not verified.** Read [what is verified](#what-is-verified) before pointing
> this at anything you care about.

```
malmok                 # TUI wizard: build, grow, resume, upgrade
malmok preflight       # read-only measurement of the nodes
malmok plan            # what would be installed; no network access
malmok apply           # build from cluster.yaml
malmok upgrade         # move RKE2 versions, one node at a time
malmok report          # audit report and DNS record sheet
```

## What it does

Give it SSH access to some machines and a `cluster.yaml` describing what you
want. It produces a working RKE2 cluster: kernel parameters, swap, firewall
and other host preparation, then RKE2 itself, the Cilium dataplane, Gateway
API, cert-manager and ArgoCD.

Three properties shape the design:

- **It observes the thing itself, not a proxy for it.** To decide whether a
  port is reachable it binds a real listener and dials it from the peer, rather
  than inferring from how a connection failed.
- **Every phase is idempotent and resumable.** An interrupted run continues; it
  does not start over.
- **The engine does not know about the screen.** It emits JSONL events and the
  TUI draws them (ADR-002). A code path that needs a terminal is a defect.

## Install

```bash
go install github.com/ryxen/malmok/cmd/malmok@latest
```

Or download a binary from [releases](https://github.com/ryxen/malmok/releases)
-- linux and darwin, amd64 and arm64, with `SHA256SUMS`. Or build it:

```bash
git clone https://github.com/ryxen/malmok
cd malmok
go build -o bin/malmok ./cmd/malmok
```

Go 1.25+. The result is a static binary; the nodes need nothing but SSH.

## Five minutes

```bash
# 1. check the document -- no node is contacted
malmok plan -f cluster.yaml --validate-only

# 2. measure the nodes -- nothing is changed
malmok preflight -f cluster.yaml

# 3. build
malmok apply -f cluster.yaml

# 4. read the result
malmok report
```

Run `malmok` with no arguments for the wizard: it writes the `cluster.yaml`
and installs from the same screen. `malmok apply --demo` walks the whole
sequence without touching a node, if you want to see the shape of a run first.

A minimal document:

```yaml
apiVersion: platform.ryxen.dev/v1alpha1
kind: ClusterSpec

metadata:
  name: my-cluster
  profile: homelab        # the profile fills in the rest

network:
  mode: online

topology:
  # One server, so there is no VIP. Saying so explicitly records the
  # trade-off: adding a second server later means re-joining every node
  # (ADR-008).
  registrationAddress: 192.0.2.10
  acceptNodeRegistration: true
  servers:
    - host: 192.0.2.10
      ssh:
        user: ubuntu
        privateKey: file://~/.ssh/id_ed25519

kubernetes:
  version: v1.36.3+rke2r1
```

That is enough for the Cilium dataplane, Gateway API and local-path storage --
the profile supplies them, and `malmok plan` prints every value it filled in
along with where it came from. More in [`examples/`](examples/).

## What is verified

An infrastructure tool that overstates its coverage breaks somebody else's
cluster. So the untested rows are in the same table as the tested ones.

| Configuration | Status | Evidence |
|---|---|---|
| Single node (control-plane + etcd) | **verified on hardware** | production install in a data centre |
| Two nodes (server + agent) | **verified on hardware** | lab harness, repeated |
| Grow / resume after interruption / re-apply | **verified on hardware** | verification matrix |
| RKE2 upgrade | **verified on hardware** | verification matrix |
| Cilium dataplane + Gateway API | **verified on hardware** | reachable from a public address |
| private-CA listener certificates | **verified on hardware** | lab harness |
| ACME (Let's Encrypt) certificates | not verified | needs public DNS and an account |
| Three-server HA (etcd quorum) | **not verified** | no hardware yet |
| Airgap / proxy | **not verified** | schema only |
| External registry mirror | **not verified** | schema only |
| Storage backends | out of scope | the application's concern |
| Observability stack | **not implemented** | schema only |

The verification matrix defines combinations across seven dimensions and
**enforces coverage with tests** -- add a value and use it in no case, and an
offline test fails. See
[`docs/40-verification-matrix.md`](docs/40-verification-matrix.md).

## What it deliberately does not do

| Not done | Why |
|---|---|
| Create HTTPRoutes | the application chart's job (ADR-006); this tool stops at the gateway |
| Install ingress-nginx | EOL 2026-03 (ADR-005) |
| Change anything during preflight | preflight measures; it does not fix |
| Restart all nodes at once | one at a time, each back to Ready first |
| Store plaintext secrets in `cluster.yaml` | references only (`file://`, `env://`) |

## Documentation

| Document | Contents |
|---|---|
| [`docs/00-architecture.md`](docs/00-architecture.md) | 13 ADRs, layering, roadmap |
| [`docs/10-preflight-plan.md`](docs/10-preflight-plan.md) | probe catalogue, downgrade decision tree |
| [`docs/11-execute.md`](docs/11-execute.md) | phase contract, idempotency, resume, event schema |
| [`docs/20-cert.md`](docs/20-cert.md) | certificate assembly, verification, renewal |
| [`docs/30-maintenance.md`](docs/30-maintenance.md) | periodic checks, reports |
| [`docs/40-verification-matrix.md`](docs/40-verification-matrix.md) | the verification matrix |
| [`docs/99-codes.md`](docs/99-codes.md) | diagnostic code registry (generated) |

The design documents are written in Korean. Logs, events and diagnostic codes
are English and stay English -- Korean log lines break grep and issue search.

## Diagnostic codes

Every failure carries a code: `PF-105` (swap is on), `PF-601` (a port is not
reachable), `EX-*` (a step failed), `UP-*` (an upgrade precondition). The
source of truth is `internal/codes/`, not the documentation --
`docs/99-codes.md` is generated from it. 145 codes are registered. Retired
numbers are never reused, because audit reports and customer tickets outlive
releases.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security reports go through
[SECURITY.md](SECURITY.md), not the issue tracker -- this tool handles SSH
credentials and kubeconfigs.

## Licence

[Apache License 2.0](LICENSE).
