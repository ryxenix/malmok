# Upgrade and recovery

Malmok treats upgrades and interrupted work as continuations of an observed
cluster state, not as fresh installations.

## Upgrade RKE2

Review the target version before applying it:

```bash
TARGET_RKE2=v1.36.3+rke2r1
malmok upgrade --to "$TARGET_RKE2"
```

Malmok upgrades one node at a time and waits for it to return to `Ready` before
moving to the next. It does not restart the whole cluster at once.

For Fish, only the variable assignment differs:

```fish
set TARGET_RKE2 v1.36.3+rke2r1
malmok upgrade --to "$TARGET_RKE2"
```

!!! info "Verification scope"

    Three servers are verified for building, for losing one of them, and for
    an upgrade: matrix `upgrade-three` builds at one minor and moves to the
    next a server at a time, with every kubelet reporting the new version
    afterwards. Check the
    [verification matrix](../40-verification-matrix.md) for what each row
    covers.

### With carried artifacts

When the document sets `kubernetes.artifactPath`, the upgrade installs from
that directory on each node and fetches nothing. The directory still holds the
release the cluster was built from, so before upgrading, replace its contents
on **every node** with the target release's artifacts: `install.sh`,
`rke2.linux-<arch>.tar.gz`, `sha256sum-<arch>.txt`, and on an air-gapped
network the images archives as well (`rke2-images.linux-<arch>.tar.zst`, and
`rke2-images-cilium.linux-<arch>.tar.zst` for a Cilium dataplane).

Before any node is drained, `UP-006` reads each node's directory and runs the
`rke2` binary inside its tarball to ask its version. A directory that is
missing, incomplete, unreadable or holds another release stops the upgrade with
the node named and nothing changed. RKE2's artifact names carry no version, so
this is the only way a directory staged for the build is told apart from one
staged for the upgrade.

!!! info "Verification scope"

    Verified by hand on real machines with the nodes' own egress dropped,
    not as a matrix case (pod egress was not blocked; see the README's
    verification table): a server and an agent built at v1.35.8+rke2r1 from carried
    artifacts, refused by `UP-006` with nothing changed while the directory
    still held v1.35.8, then moved to v1.36.4+rke2r1 once it was restaged.
    Three servers, and an air-gapped upgrade that fails or is interrupted part
    way, have not been run. Releases before this change did not read the
    artifact path during an upgrade at all.

## Resume an interrupted run

Every apply writes durable state and a JSONL event stream under its run
directory. Resume with the identifier Malmok printed:

```bash
malmok apply --resume RUN_ID
```

Resume refuses to continue if the input document's digest changed. This keeps
an interrupted run from silently completing against a different desired state.

## Reapply the same document

```bash
malmok apply -f cluster.yaml
```

Phases are idempotent: each checks the target condition before changing it.
Reapplying the same document should converge without rebuilding healthy work.

## Stopping a server on a cluster with a VIP

`rke2-killall.sh` stops RKE2 by killing its containers outright, and kube-vip
is one of them. Killed that way it cannot release the VIP, so the address stays
on that server's network interface after RKE2 is gone. Another server takes the
address over, and until the stopped server's own kube-vip starts again -- when
RKE2 does, or the machine reboots -- both of them answer for it. Requests made
on the stopped server go to itself and are refused, and other machines on the
segment can be sent there too.

This was measured, not reasoned about. In the three-server failover case the
stopped server still held the address thirty seconds after another had claimed
it, and released it only when its own kube-vip restarted.

A server that is going to stay stopped should have the address taken off. Find
the interface that carries it, then remove it:

```bash
ip -4 -o addr show | grep -F <VIP>
sudo ip addr del <VIP>/32 dev <interface>
```

A reboot does the same on its own, which is why a server that fails outright
does not leave this behind.

## Restore etcd from a snapshot

Malmok has no restore command. Restoring is RKE2's own cluster reset, and the
steps below are the ones rehearsed on the lab.

### What to keep, off the servers

- the snapshot file, from `kubernetes.etcd.snapshotTarget` or
  `/var/lib/rancher/rke2/server/db/snapshots`;
- the server token, `/var/lib/rancher/rke2/server/token`;
- `/etc/rancher/rke2/`, which holds `config.yaml` and `registries.yaml`;
- `/var/lib/rancher/rke2/server/manifests/`, the manifests Malmok wrote;
- the RKE2 version, and on an air-gapped site its release artifacts.

!!! danger "The snapshot and the token together unlock the cluster's secrets"

    A snapshot holds the cluster's CA keys and its secrets. Anyone holding a
    snapshot and the server token can read them, so keep the two protected,
    and apart where you can.

To take a snapshot on demand before maintenance:

```bash
sudo rke2 etcd-snapshot save --name before-maintenance
```

### On the same server

Replace `SNAPSHOT` with the file's name:

```bash
sudo systemctl stop rke2-server
sudo rke2 server --cluster-reset --cluster-reset-restore-path=/var/lib/rancher/rke2/server/db/snapshots/SNAPSHOT
sudo systemctl start rke2-server
```

The reset exits by itself. Its last line, "Managed etcd cluster membership has
been reset, restart without --cluster-reset flag now", is logged at error level
and is the expected end.

### When the server is lost

On a replacement with the same address:

1. Install the same RKE2 version. On an air-gapped site, from the carried
   artifacts with `INSTALL_RKE2_ARTIFACT_PATH`.
2. Put back `/etc/rancher/rke2/` and `/var/lib/rancher/rke2/server/manifests/`.
3. Put the snapshot file under `/var/lib/rancher/rke2/server/db/snapshots/`.
4. Reset with the saved token, then start the service. `TOKEN_FILE` is where
   you put the saved token:

```bash
sudo rke2 server --cluster-reset --cluster-reset-restore-path=/var/lib/rancher/rke2/server/db/snapshots/SNAPSHOT --token="$(sudo cat TOKEN_FILE)"
sudo systemctl enable --now rke2-server
```

In Fish, the token is `--token=(sudo cat TOKEN_FILE)`. If `config.yaml` sets a
token, it has to be the saved one, or RKE2 does not start. Agents rejoin on
their own: they point at the registration address and carry the agent token.

### With more than one server

Not rehearsed. RKE2's procedure is to stop `rke2-server` on every server, reset
on one and start it, then on each of the others delete
`/var/lib/rancher/rke2/server/db/` and start it.

!!! info "Verification scope"

    Rehearsed by hand on the lab, not as a matrix case: one server and one
    agent on v1.36.4, with egress allowed. In place, and onto the same server
    wiped and reinstalled from the saved token, configuration and manifests.
    Both times a ConfigMap made before the snapshot came back, one made after
    it was gone, both nodes were Ready and the agent had rejoined. Three
    servers and an air-gapped restore have not been rehearsed. On a cluster
    using the embedded mirror, `rke2 etcd-snapshot save` prints "Unknown flag
    --embedded-registry found in config.yaml, skipping"; the snapshot was
    taken and restored regardless.

## Read the evidence first

For certificate maintenance, v0.96.4 and later support
[read-only expiry scans](certificates.md). This does not restart RKE2 or renew
certificates; it creates a separate run whose report can be generated by ID.

```bash
malmok report
```

When diagnosing a failure, start with the diagnostic code and the tail of the
run's `events.jsonl`. Codes are stable and retired numbers are not reused, so
reports and incident tickets remain meaningful after later releases.

[Look up a diagnostic code →](../99-codes.md)
