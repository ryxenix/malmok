# Configuration reference

`cluster.yaml` is both desired state and an audit artifact. Malmok rejects
unknown fields rather than silently ignoring a misspelling.

```bash
malmok plan -f cluster.yaml --validate-only
```

## Document identity

Every document begins with:

```yaml
apiVersion: malmok.dev/v1alpha1
kind: ClusterSpec
```

The schema is `v1alpha1`. Fields can change between minor releases; read the
[changelog](https://github.com/ryxenix/malmok/blob/main/CHANGELOG.md) before
upgrading Malmok.

## Profiles

Profiles fill omitted values but never override values explicitly selected by
the operator. `malmok plan` reports both the resolved value and its source.

| Profile | Intended starting point |
|---|---|
| `homelab` | Online Ubuntu cluster with Cilium Gateway API and local-path storage |
| `company-prod` | Online company cluster with production-oriented platform defaults |
| `onprem-dmz` | Proxied or allowlisted on-premises network |
| `airgap-ubuntu` | Air-gapped Ubuntu nodes |
| `airgap-rocky` | Air-gapped Rocky nodes |
| `airgap-conservative` | Air-gapped Rocky baseline without an eBPF dataplane |
| `custom` | No baseline; requires explicit acknowledgement during plan approval |

## Top-level sections

| Section | Responsibility |
|---|---|
| `metadata` | Cluster name, profile and site annotations |
| `network` | Online, proxy or air-gap mode; pod/service networks and routing |
| `topology` | Registration endpoint, VIP, servers, agents and SSH access |
| `os` | Host family, NTP, swap policy and hardening prerequisites |
| `kubernetes` | RKE2 version, offline artifact path, dataplane and etcd |
| `pki` | Certificate source and trust distribution |
| `registry` | Image registry, mirrors, credentials and chart source |
| `storage` | Local path, or a site-owned CSI implementation. Longhorn and NFS are in the schema and are **not installed by this release**: a document naming one stops the run and says so, rather than leaving the cluster with no StorageClass |
| `gateway` | GatewayClass, addresses, listeners, TLS and DNS report settings |
| `platform` | GitOps and observability components |
| `output` | Audit report, run bundle and event-log locations |

## Declared settings and applied settings

Schema validation checks the document, not whether every declared feature is
implemented or active on a node. On current `main`, `os.hardening.cisProfile`
and `prepareCISPrerequisites` have preflight/report handling but are not wired
to apply the CIS profile and its full prerequisites. Do not rely on those
fields to change a running cluster; check its effective configuration
separately.

`kubernetes.etcd.snapshotSchedule`, `snapshotRetention` and `snapshotTarget`
are written to every server's RKE2 configuration (`etcd-snapshot-schedule-cron`,
`etcd-snapshot-retention`, `etcd-snapshot-dir`). Left unset, RKE2's defaults
apply: every twelve hours, five kept, on each node's own disk. The target is a
directory on each server; a mounted backup directory has to exist before the
server starts. `kubernetes.etcd.s3` is reserved and refused at validation --
Malmok writes no S3 settings. Releases before this change declared these
fields without writing them.

`topology[].gpu` is different: it is refused rather than ignored. Malmok
installs no device plugin, no RuntimeClass and no GPU container runtime, so a
document setting it is rejected at validation instead of producing a cluster
whose pods cannot see the hardware the document named. Install the vendor's
operator yourself -- NVIDIA's GPU Operator or AMD's equivalent -- after the
cluster is up.

## Secret references

Do not put plaintext credentials in `cluster.yaml`. Secret-bearing fields use
references such as:

```yaml
registry:
  username: env://REGISTRY_USER
  password: file://./secrets/registry-password
```

The document records where a secret came from without taking ownership of its
plaintext value.

## Discover the effective configuration

```bash
# Syntax and schema only; no network access
malmok plan -f cluster.yaml --validate-only

# Resolve the profile and measure the actual nodes
malmok plan -f cluster.yaml
```

For working documents, start from the repository's
[`examples/`](https://github.com/ryxenix/malmok/tree/main/examples) directory.
The Go types under
[`api/v1alpha1`](https://github.com/ryxenix/malmok/tree/main/api/v1alpha1) are
the schema source of truth while the full generated field reference is being
prepared.
