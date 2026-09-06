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

## Read the evidence first

```bash
malmok report
```

When diagnosing a failure, start with the diagnostic code and the tail of the
run's `events.jsonl`. Codes are stable and retired numbers are not reused, so
reports and incident tickets remain meaningful after later releases.

[Look up a diagnostic code →](../99-codes.md)

