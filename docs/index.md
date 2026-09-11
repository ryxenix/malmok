---
hide:
  - toc
---

<div class="malmok-hero" markdown>

![Malmok](img/malmok-wordmark.png){ .malmok-hero__wordmark }

# Build RKE2 clusters you can repeat, resume and account for

<p class="malmok-hero__lead">
Malmok turns a <code>cluster.yaml</code> and SSH access into an RKE2 cluster on
infrastructure you control. It measures before changing, resumes interrupted
runs and leaves evidence an operator can hand over.
</p>

<div class="malmok-hero__actions">
  <a href="getting-started/quick-start/" class="md-button md-button--primary">Quick start</a>
  <a href="guides/air-gap/" class="md-button">Air-gapped install</a>
  <a href="https://github.com/ryxenix/malmok" class="md-button">View on GitHub</a>
</div>

<p class="malmok-kicker">Bare metal · VMs · On-premises · DMZ · Air-gapped (partly verified)</p>

</div>

!!! warning "Alpha software"

    Try Malmok in a homelab or an evaluation environment and send feedback.
    Before using it for work, read the verification scope and its limits.

    Single-node and two-node clusters are verified repeatedly on real
    hardware, upgrade, resume and re-apply included. Three-server HA, proxy
    networks and external registry mirrors are not. The air-gapped path is
    verified: RKE2, the charts and every image from carried files, with egress
    dropped. The document schema is `v1alpha1` and can change between minor
    releases.

    The version is a compatibility promise, not a maturity score. **1.0 is
    when the schema becomes `v1` and the rows marked *not verified* say
    otherwise.**

## Is Malmok for you?

Start with existing Linux machines and SSH access. Malmok helps you build and
manage RKE2 from a configuration file, whether the cluster is yours or your team's.

### Personal projects and homelabs

For running your own RKE2 cluster on home servers, mini PCs or personal VMs:

- Rebuild from a configuration file without looking up manual installation steps each time.
- Reduce repetitive setup when experimenting with nodes or reinstalling a lab.
- Start from your workstation with SSH and a CLI, without a separate management cluster.

[Build your first cluster →](getting-started/quick-start.md)

### Work and team environments

For solution vendors, systems integrators and deployment engineers who repeatedly
deliver software to customer-provided servers, as well as internal infrastructure teams.
**RKE2 has been selected, but the cluster foundation still needs to be built.**

- Standardize RKE2 setup across customer sites when delivering AI, document-processing, search or data platforms.
- Check real node conditions and blockers before making changes.
- Resume interrupted builds and keep execution records for review and handoff.
- Prepare for a restricted network after checking the supported air-gap conditions.

Prepared servers still need site-specific checks, artifacts and handoff records.
The developer used an early Malmok version on real data-centre servers; that
experience informs these workflows, not a guarantee for every business environment.

[On-premises checklist →](guides/on-premises.md) · [Air-gap requirements →](guides/air-gap.md) · [Upgrade and recovery →](guides/upgrade-and-recovery.md)

This can include public-sector and state-owned enterprise delivery projects with
the same starting conditions. It is a deployment use case, not a claim of procurement
eligibility or security certification. [Customer-site suitability and responsibilities →](about/comparison.md#customer-delivery)

**Malmok is alpha software. Both groups should review the
[verification scope](40-verification-matrix.md) before adoption; these use cases
are not a production-readiness guarantee.** It does not provision VMs or replace
general configuration management or application deployment tools.

<div class="grid cards" markdown>

-   :material-radar:{ .lg .middle } **Measure first**

    ---

    Bind real listeners, dial from peers and inspect the nodes before a
    mutating phase begins.

-   :material-backup-restore:{ .lg .middle } **Resume safely**

    ---

    Continue an interrupted phase instead of rebuilding a half-finished
    cluster from the beginning.

-   :material-file-document-check:{ .lg .middle } **Audit the result**

    ---

    Keep JSONL events, stable diagnostic codes and a handoff report describing
    what ran and why.

</div>

## Install

The command is the same in Bash and Fish. The downloaded installer runs under
POSIX `sh`, selects the release for Linux or macOS and amd64 or arm64, and
checks the binary against the published `SHA256SUMS`.

```bash
curl -fsSL https://malmok.dev/install.sh | sh
```

[Review all installation options →](getting-started/installation.md)

## Start with the wizard

After installing Malmok, run the wizard without creating a configuration file:

```bash
malmok apply --tui
```

Enter your node addresses and SSH access details. The wizard creates
`cluster.yaml` and guides you through checks and installation.

## Configuration files for repeatable runs { #start-with-one-document }

This is an alternative to the wizard, not a prerequisite for it.

Save the following as `cluster.yaml`, replacing the documentation address,
SSH account, key and RKE2 version with values from your environment.

```yaml title="cluster.yaml"
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

The `homelab` profile supplies the Cilium dataplane, Gateway API and local-path
storage. `malmok plan` prints every resolved value and where it came from.

```bash
# Validate the document without contacting a node
malmok plan -f cluster.yaml --validate-only

# Measure the nodes without changing them
malmok preflight -f cluster.yaml

# Review the resolved plan
malmok plan -f cluster.yaml

# Build, then inspect the handoff report
malmok apply -f cluster.yaml
malmok report
```

## See the complete flow

The recording uses demo mode, so it touches no node. The same engine runs a
real build with `malmok apply -f cluster.yaml`.

<div class="malmok-demo" markdown>
  ![Malmok TUI from configuration through preflight, installation and result](img/tui-wizard.gif)
</div>

## One tool, a clear boundary

Malmok prepares hosts and installs RKE2, the dataplane, Gateway API,
cert-manager, Argo CD and a VictoriaMetrics stack. It does not provision
machines, change firewall policy or deploy your applications.

If you need general configuration management, use Ansible. If you already run
a management cluster and Cluster API, CAPRKE2 may be a better fit. Malmok is
for the operator starting from SSH-reachable machines who wants no management
controller or node agent left behind.

[Read the FAQ and tool comparisons →](about/comparison.md)

## Why “말목”?

`말목` (*malmok*, roughly “mal-mok”) is a **Korean word** for a wooden stake
driven firmly into the ground to mark a boundary or reinforce a foundation.
The name reflects the job: anchor the first server, then bring the cluster
together around a dependable base.
