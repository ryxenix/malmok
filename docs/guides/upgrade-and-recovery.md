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

!!! warning

    Three-server HA and etcd quorum behavior are not yet hardware-verified by
    this project. Check the [verification matrix](../40-verification-matrix.md)
    before using an unverified topology.

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

## Read the evidence first

```bash
malmok report
```

When diagnosing a failure, start with the diagnostic code and the tail of the
run's `events.jsonl`. Codes are stable and retired numbers are not reused, so
reports and incident tickets remain meaningful after later releases.

[Look up a diagnostic code →](../99-codes.md)

