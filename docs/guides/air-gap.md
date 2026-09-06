# Air-gapped installation

An air-gapped build is more than installing the `malmok-airgap` binary. Every
artifact the cluster consumes must cross the boundary deliberately, and the
document must name where each one was placed.

Malmok's air-gap path has been verified in a dedicated two-node hardware run
with egress rejected. It is not yet a dimension in the general verification
matrix.

## What must cross the boundary

Prepare these on a connected staging machine:

- the Linux `malmok-airgap` binary for the operator architecture;
- the RKE2 tarball, its checksum file and the upstream RKE2 `install.sh`;
- the RKE2 image archives required by the selected dataplane;
- the Gateway API bundle when Gateway API CRDs are enabled;
- every application image the cluster will run; and
- mirrored Helm charts for cert-manager, observability or Argo CD when those
  components are enabled.

The release's air-gap Malmok binary embeds Helm and k9s for amd64 or arm64. It
does not embed RKE2, Kubernetes images or your application payloads.

## Stage the nodes

Place the RKE2 artifacts in the same directory on each node. The exact list
depends on the RKE2 version and dataplane; `malmok preflight` reports every
missing filename before the mutating install begins.

The directory normally contains:

```text
/srv/malmok/rke2/
├── install.sh
├── rke2.linux-amd64.tar.gz
├── sha256sum-amd64.txt
├── rke2-images-*.linux-amd64.tar.zst
└── gateway-api-*-standard-install.yaml
```

Use `arm64` artifacts on arm64 nodes. Do not infer the required image set from
this illustrative listing; let preflight evaluate it against the chosen RKE2
version and dataplane.

## Describe the offline sources

The important part of an air-gapped document looks like this:

```yaml
metadata:
  profile: airgap-ubuntu

network:
  mode: airgap

os:
  ntpServers:
    - 10.20.0.10

kubernetes:
  version: v1.36.3+rke2r1
  artifactPath: /srv/malmok/rke2

registry:
  mode: internal
  systemDefaultRegistry: harbor.internal.example
  chartRepo: oci://harbor.internal.example/charts
```

This is a fragment, not a complete `cluster.yaml`. Add topology, PKI, storage
and platform settings for the site. If RKE2's image archives are preloaded on
the nodes, an embedded registry can be used instead of rewriting system images
to an internal registry.

!!! danger "A chart mirror and an image mirror are different inputs"

    `systemDefaultRegistry` redirects container images. `chartRepo` tells
    Malmok where Helm chart definitions live. Mirroring only one produces an
    installation that still attempts egress.

## Validate the hand-carried set

Run the non-mutating gates before entering the installation window:

```bash
malmok plan -f cluster.yaml --validate-only
malmok preflight -f cluster.yaml
malmok plan -f cluster.yaml
```

Air-gap-specific preflight checks cover the artifact directory, image sources,
chart source, NTP reachability and certificate-chain behavior without relying
on a public network.

## Install and preserve the evidence

```bash
malmok apply -f cluster.yaml
malmok report
```

Keep the input document, release checksums, event stream and handoff report
together. They are the evidence needed to reproduce or audit the build after
the original installation window has closed.

