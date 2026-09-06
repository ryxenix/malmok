<h1 align="center">Malmok (말목)</h1>

<p align="center"><img src="docs/img/malmok-wordmark.png" alt="Malmok — an anchored cluster" width="620"></p>

<p align="center">
  <a href="https://github.com/ryxenix/malmok/actions/workflows/ci.yml"><img src="https://img.shields.io/badge/CI-GitHub_Actions-2088FF?logo=githubactions&amp;logoColor=white" alt="CI: GitHub Actions"></a>
  <img src="https://img.shields.io/badge/status-alpha-f59e0b" alt="Status: alpha">
  <img src="https://img.shields.io/badge/Go-1.25.8-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.25.8">
  <a href="LICENSE"><img src="https://img.shields.io/badge/licence-Apache--2.0-blue" alt="Apache-2.0 licence"></a>
</p>

<p align="center"><strong>Build RKE2 clusters on infrastructure you control — repeatably, resumably and without installing agents.</strong></p>

<p align="center"><sub>Bare metal · VMs · On-premises · DMZ · Air-gapped</sub></p>

<p align="center">
  <a href="#five-minutes"><strong>Get started</strong></a> ·
  <a href="#what-is-verified"><strong>Verification</strong></a> ·
  <a href="https://malmok.dev"><strong>Docs</strong></a> ·
  <a href="https://github.com/ryxenix/malmok/releases"><strong>Releases</strong></a>
</p>

<p align="center"><em><a href="README.ko.md">한국어</a></em></p>

<p align="center"><img src="docs/img/tui-wizard.gif" alt="The Malmok wizard, start to finish: where, nodes, checks, install, result" width="900"></p>

<p align="center"><sub><code>malmok apply --tui</code> in demo mode — the same engine runs headless with <code>malmok apply -f cluster.yaml</code>.</sub></p>

| Measure first | Resume safely | Audit everything |
|:---:|:---:|:---:|
| Probe the real nodes and network before changing them | Continue an interrupted run instead of starting over | Keep stable diagnostic codes, JSONL events and handoff reports |

> **Status: alpha.** Single-node and two-node clusters are verified repeatedly
> on real hardware, and the tool has built a production cluster in a data
> centre. **Three-server HA and external registries are not verified.** Read
> [what is verified](#what-is-verified) first. The document schema is
> `v1alpha1` and may change between minor releases; `malmok plan
> --validate-only` names every field it does not recognise.

## Who it is for

Malmok is for people who want to build and manage RKE2 on existing Linux
machines using a `cluster.yaml` and SSH access.

### Personal projects and homelabs

For running your own RKE2 cluster on home servers, mini PCs or personal VMs:

- Rebuild from a configuration file without looking up manual installation steps each time.
- Reduce repetitive setup when experimenting with nodes or reinstalling a lab.
- Start from your workstation with SSH and a CLI, without a separate management cluster.

### Work and team environments

For solution vendors, systems integrators and deployment engineers who repeatedly
deliver software to customer-provided servers, as well as internal infrastructure teams.
The starting point is a decision to use RKE2, with the cluster foundation still to build.

- Standardize RKE2 setup across customer sites when delivering AI, document-processing, search or data platforms.
- Check real node conditions and blockers before making changes.
- Resume interrupted builds and keep execution records for review and handoff.
- Prepare an installation for a restricted network after checking the [air-gap requirements and verification limits](docs/guides/air-gap.md).

**Both groups should check the [verification scope](#what-is-verified) before
adoption. Malmok is alpha software; these use cases are not a production-readiness guarantee.**

Malmok does not provision VMs or replace general configuration management or
application deployment tools. It builds and operates the cluster foundation;
you remain responsible for the machines and applications.

## Start with one document

This is the smallest useful shape of a Malmok cluster. Replace the
documentation address, SSH account, key and RKE2 version with values for your
environment, then save it as `cluster.yaml`.

> This is not RKE1's `cluster.yml`. The two formats are unrelated: `rke up`
> cannot read this file, and Malmok cannot read that one.

```yaml
apiVersion: malmok.dev/v1alpha1
kind: ClusterSpec

metadata:
  name: my-cluster
  profile: homelab

network:
  mode: online

topology:
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

The profile supplies the Cilium dataplane, Gateway API and local-path storage;
`malmok plan` prints every resolved value and its source. A single server's
address is not a stable registration endpoint, so introduce a stable DNS name
or virtual IP before adding servers. More configurations are in
[`examples/`](examples/).

## 말목 — the name

`말목` (*malmok*, roughly “mal-mok”) is a Korean word for a wooden stake driven
firmly into the ground to mark a boundary or reinforce a foundation. The name
fits the tool's role: establishing a dependable base, anchoring the first
server and bringing the rest of the cluster together around it.

## One engine, two ways to work

Use the TUI when you want Malmok to guide the run. Use the CLI when the same
work needs to be reviewed, repeated or automated.

```bash
# TUI wizard: build, grow, resume, upgrade
malmok apply --tui

# Validate the document offline
malmok plan -f cluster.yaml --validate-only

# Read-only measurement of the nodes
malmok preflight -f cluster.yaml

# Measure nodes and show the install plan
malmok plan -f cluster.yaml

# Build from the document
malmok apply -f cluster.yaml

# Choose the target version and move versions one node at a time
TARGET_RKE2=v1.36.3+rke2r1
malmok upgrade --to "$TARGET_RKE2"

# Generate the audit report and DNS record sheet
malmok report
```

<details>
<summary>Fish shell</summary>

```fish
# TUI wizard: build, grow, resume, upgrade
malmok apply --tui

# Validate the document offline
malmok plan -f cluster.yaml --validate-only

# Read-only measurement of the nodes
malmok preflight -f cluster.yaml

# Measure nodes and show the install plan
malmok plan -f cluster.yaml

# Build from the document
malmok apply -f cluster.yaml

# Choose the target version and move versions one node at a time
set TARGET_RKE2 v1.36.3+rke2r1
malmok upgrade --to "$TARGET_RKE2"

# Generate the audit report and DNS record sheet
malmok report
```

</details>

## What it does

Give it SSH access to some machines and a `cluster.yaml` describing what you
want. It produces a working RKE2 cluster: kernel parameters, swap, data
directories and other host preparation, then RKE2 itself, the Cilium dataplane, Gateway
API, cert-manager, ArgoCD and a VictoriaMetrics stack scraping the cluster.
On the first server, it also places `kubectl`, `helm` and `k9s` on the PATH of
the account used to operate the cluster. RKE2 buries kubectl where nothing
finds it and ships no helm CLI at all, so a finished install used to hand you
credentials to a cluster you could not address.

Three properties shape the design:

- **It observes the thing itself, not a proxy for it.** To decide whether a
  port is reachable it binds a real listener and dials it from the peer, rather
  than inferring from how a connection failed.
- **Every phase is idempotent and resumable.** An interrupted run continues; it
  does not start over.
- **The engine does not know about the screen.** It emits JSONL events and the
  TUI draws them. A code path that needs a terminal is a defect.

## FAQ

### What if the customer has already prepared the servers?

That is where Malmok starts. Given existing Linux servers and SSH access, it
checks the environment, shows the RKE2 build plan, installs the cluster,
resumes interrupted runs and produces a handoff report. No particular server
provisioning tool is required.

### Could I use Terraform or Ansible instead?

Yes. If you already have reliable RKE2 automation, you do not need to replace
it. Malmok is not an alternative to IaC as a practice: it uses a declarative
`cluster.yaml` and packages RKE2-specific checks, execution, recovery and
reporting so you do not have to assemble and maintain that workflow yourself.

### How is this different from k0sctl or CAPRKE2?

k0sctl targets k0s; Malmok targets RKE2. If you already operate a management
cluster and Cluster API, consider CAPRKE2. Malmok runs from your workstation
against existing SSH-reachable machines without a Malmok management controller
or node agent.

### Can I use it in production today?

Malmok is alpha software, not a production-readiness guarantee. Review the
[verified and unverified configurations](#what-is-verified) and validate your
own environment before adoption.

[Read the full FAQ and tool comparisons →](docs/about/comparison.md)

## Install

Install the latest release with one command:

```bash
curl -fsSL https://malmok.dev/install.sh | sh
```

The command is the same in Bash and Fish. The downloaded installer itself runs
under POSIX `sh`.

The installer detects Linux or macOS and amd64 or arm64, downloads the matching
[release](https://github.com/ryxenix/malmok/releases), verifies it against the
published `SHA256SUMS`, and installs `malmok` in `/usr/local/bin`. To inspect
the script before running it:

```bash
curl -fsSLo install-malmok.sh https://malmok.dev/install.sh
less install-malmok.sh
sh install-malmok.sh
```

Pass `--version vX.Y.Z`, `--bin-dir ~/.local/bin`, or `--airgap` after
`sh -s --` to select a release, installation directory, or Linux air-gap
binary. For example:

```bash
curl -fsSL https://malmok.dev/install.sh | sh -s -- --version v0.83.0
```

You can also download a binary directly from the releases page -- Linux and
macOS, amd64 and arm64 are provided -- and verify it against the included
`SHA256SUMS`.

After downloading the binary, set `MALMOK_BIN` to its filename and install it
on your PATH. For example, on Linux amd64:

```bash
# Replace X.Y.Z with the release version
MALMOK_BIN=./malmok_vX.Y.Z_linux_amd64
chmod +x "$MALMOK_BIN"
sudo install "$MALMOK_BIN" /usr/local/bin/malmok
malmok --version
```

The equivalent manual installation in Fish is:

```fish
# Replace X.Y.Z with the release version
set MALMOK_BIN ./malmok_vX.Y.Z_linux_amd64
chmod +x "$MALMOK_BIN"
sudo install "$MALMOK_BIN" /usr/local/bin/malmok
malmok --version
```

Or install from source:

```bash
git clone https://github.com/ryxenix/malmok
cd malmok
go build -o bin/malmok ./cmd/malmok
```

This requires Go 1.25.8 or newer. The following works too:

```bash
go install github.com/ryxenix/malmok/cmd/malmok@latest
```

A `go install` build is not stamped with the release version and reports `dev`
from `malmok --version`; use a release binary when the exact version matters.

## Requirements

- Run Malmok from Linux or macOS on amd64 or arm64.
- Target nodes must be Ubuntu or Rocky/RHEL-family Linux on amd64 or arm64.
  The verified profiles currently cover Ubuntu 22.04/24.04 and Rocky 9; see
  [what is verified](#what-is-verified) before using another combination.
- The operator needs SSH access and root access or working privilege
  escalation on every target node. A local single-node install can be run as
  root without SSH.
- Each node needs at least 20 GB free disk space; 2 CPU cores, 4 GB RAM and 50
  GB free disk are recommended. Preflight reports the exact blockers and
  recommendations.
- Nodes must reach each other on 6443 (Kubernetes API), 9345 (the RKE2
  supervisor -- absent from every Kubernetes port reference, and the one people
  miss), 2379-2380 (etcd, between servers) and 10250 (kubelet), plus the
  dataplane's own ports. With `registry.mode: embedded`, also 5001: that is how
  the nodes advertise which images they hold, and a cluster with it closed does
  not fail -- every pull goes to the upstream registry instead, which on an
  air-gapped node is a pull that hangs. Preflight does not infer any of this:
  it binds a real listener on one node and dials it from the peer.

## What it changes on a node

Malmok needs root because it prepares the host. It is worth knowing what that
means before granting it, so the whole list is here rather than in the source:

- Loads `br_netfilter` and `overlay`, and writes
  `/etc/modules-load.d/90-malmok.conf` so the choice survives a reboot.
- Writes `/etc/sysctl.d/90-malmok.conf`: IPv4 and IPv6 forwarding, bridge
  netfilter for both families, and raised inotify limits.
- Turns swap off and comments it out of `/etc/fstab`, keeping a backup at
  `/etc/fstab.malmok.bak`. Both halves: the kubelet refuses to start with swap
  on, and an entry left in fstab brings it back at the next boot. A site that
  keeps its swap sets `os.disableSwap: false`, and then this step does not run
  at all -- the document also has to pass `fail-swap-on=false` in
  `kubernetes.kubeletArgs`, which validation requires rather than adds.
- Creates `/var/lib/rancher`, the directory RKE2 grows into.
- Installs RKE2 from a release tarball and manages its systemd unit.
- Writes cluster manifests under `/var/lib/rancher/rke2/server/manifests`.
- On the first server, places `kubectl`, `helm` and `k9s` on the operating
  account's PATH.
- Only when the document asks for them: installs a private CA into the node
  trust store, and writes containerd's registry configuration (mode 0600,
  because it can hold a registry password).

It does **not** modify the firewall. Preflight reports an active firewall and
the ports that have to be open; opening them belongs to whoever owns the
policy. It does not reboot nodes either -- an upgrade restarts services one
node at a time, waiting for Ready in between.

## Five minutes

`malmok apply --tui` opens the wizard instead: it writes the `cluster.yaml`
shown above and installs from the same screen, in English or Korean (`--lang
ko`, or the settings screen, which remembers). `malmok apply --demo` simulates
the main installation flow and TUI states without touching a node.

Then validate, measure, build and inspect the result:

```bash
# 1. check the document -- no node is contacted
malmok plan -f cluster.yaml --validate-only

# 2. measure the nodes -- nothing is changed
malmok preflight -f cluster.yaml

# 3. measure the nodes and review the resolved installation plan
malmok plan -f cluster.yaml

# 4. build
malmok apply -f cluster.yaml

# 5. read the result
malmok report
```

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
| ACME HTTP-01 certificates | **verified on hardware** | Let's Encrypt issued on the production cluster; TLS 1.3, chain and hostname checked |
| ACME DNS-01 certificates (wildcards) | not verified | needs a credential for the DNS zone |
| Three-server HA (etcd quorum) | **not verified** | no hardware yet |
| Airgap install (no egress) | **verified on hardware** | dedicated two-node lab run outside the general matrix; egress dropped rather than rejected, the way a site firewall behaves; RKE2, Cilium and Gateway API from carried artifacts, and every outbound attempt logged and accounted for |
| Proxy | **not verified** | schema only |
| External registry mirror | **not verified** | schema only |
| Storage backends | out of scope | the application's concern |
| Observability (VictoriaMetrics) | **verified on hardware** | installed on the production cluster; 19 scrape targets, samples stored |

The verification matrix defines combinations across seven dimensions and
**enforces coverage with tests** -- add a value and use it in no case, and an
offline test fails. Network mode is not yet one of those dimensions; the
air-gap claim above comes from its dedicated hardware run. See
[`docs/40-verification-matrix.md`](docs/40-verification-matrix.md).

## What it deliberately does not do

| Not done | Why |
|---|---|
| Create HTTPRoutes | the application chart's job; this tool stops at the gateway |
| Install ingress-nginx | EOL 2026-03; Malmok installs the Gateway API instead |
| Change anything during preflight | preflight measures; it does not fix |
| Restart all nodes at once | one at a time, each back to Ready first |
| Store plaintext secrets in `cluster.yaml` | references only (`file://`, `env://`) |
| Provision machines | Malmok starts from hosts that already answer SSH; VMs, networks and DNS records belong to OpenTofu, Proxmox or the site's own tooling |
| Deploy applications | ArgoCD is installed and pointed at your repository; what it syncs is yours |
| Modify the node firewall | preflight reports an active firewall and the ports it needs; the policy belongs to whoever owns it |
| Operate the cluster afterwards | it installs a metrics stack and hands over a kubeconfig; alerting, dashboards and day-2 are not this tool |

## Documentation

The official documentation is at [malmok.dev](https://malmok.dev). It combines
the project overview with the installation and operating guides; the README
remains the shorter evaluation path.

- [Installation](docs/getting-started/installation.md)
- [Quick start](docs/getting-started/quick-start.md)
- [Air-gapped installation](docs/guides/air-gap.md)
- [Configuration reference](docs/reference/configuration.md)
- [Verification matrix](docs/40-verification-matrix.md)
- [Diagnostic code registry](docs/99-codes.md)

## Diagnostic codes

Operational checks and execution failures carry stable codes: `PF-105` (swap
is on), `PF-601` (a port is not reachable), `EX-*` (a step failed), `UP-*` (an
upgrade precondition). The source of truth is `internal/codes/`, not the
documentation -- [`docs/99-codes.md`](docs/99-codes.md) is generated from it.
Retired numbers are never reused, because audit reports and customer tickets
outlive releases.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security reports go through
[SECURITY.md](SECURITY.md), not the issue tracker -- this tool handles SSH
credentials and kubeconfigs.

## Licence

[Apache License 2.0](LICENSE).
