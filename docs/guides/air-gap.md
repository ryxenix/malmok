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
- every application image the cluster will run;
- the images the platform pulls. The release carries them already:
  `malmok-images_<tag>_linux_<arch>.tar.zst` holds every one, built from the
  same list the binary prints. Put it on each node in
  `/var/lib/rancher/rke2/agent/images/` before RKE2 starts, and containerd
  imports it the way it imports RKE2's own archives. To check the list rather
  than carry the bundle, `malmok images -f cluster.yaml` prints it and answers
  on a machine with no network; `images.txt` in the release is the same list
  for planning before you have anywhere to run the binary; and
- the Helm charts for cert-manager, observability or Argo CD when those
  components are enabled -- either mirrored, or carried as `.tgz` files and
  named with `registry.chartDir`.

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
and platform settings for the site.

A site with no registry of its own does not have to stand one up. Carry the
chart archives instead and name the directory holding them:

```yaml
registry:
  mode: embedded
  chartDir: ./charts        # cert-manager-v1.21.1.tgz, and the rest
```

Malmok reads them next to the document and embeds each one in its HelmChart, so
nothing is fetched at install time. PF-710 names the exact files to stage, and
names a wrong version separately -- a directory that looks right is the
mistake worth catching before the install window.

With `registry.mode: embedded`, RKE2's own registry mirror is enabled and the
nodes share images peer to peer. Images preloaded from a tarball are pinned and
shared under whatever registry they are tagged for, including one that does not
exist, so a bundle seeded onto one node reaches the rest: staging is per
cluster, not per node. The mirror needs TCP 5001 open between nodes, and a
closed 5001 does not fail -- containerd falls back to the upstream registry,
which on a closed node is a pull that hangs.

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

