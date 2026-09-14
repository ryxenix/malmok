# Certificate expiry and handoff

`malmok certs` reads the certificates RKE2 stores on server nodes and records
their expiry dates. Use it when handing over a cluster or planning maintenance.
It does not change the cluster, renew certificates or restart services.

!!! info "Available on main"

    This command and the certificate handoff fields are on current `main`,
    after v0.96.3. They are not in that release's installer download.
    See the [changelog](https://github.com/ryxenix/malmok/blob/main/CHANGELOG.md)
    and [source installation](../getting-started/installation.md) before using them.

## Run a scan

Use the cluster's configuration and an account with permission to read RKE2's
certificate directory. The command uses the document's local/SSH connection
and privilege-escalation settings; it does not grant access by itself.

```bash
# Show alerts and measurement problems
malmok certs -f cluster.yaml

# Include every measured certificate
malmok certs -f cluster.yaml -v

# Output structured findings and problems on stdout
malmok certs -f cluster.yaml -o json
```

These commands are identical in Bash and Fish. JSON output contains a list of
node scans, each with `node`, `findings` and `problems`. Run information is printed
on stderr so it does not mix with the JSON document.

## Understand the result

| Exit code | Meaning |
|---|---|
| `0` | No applicable measurement problems or alert thresholds crossed |
| `2` | A measured certificate crossed an alert threshold or expired |
| `3` | An applicable certificate measurement could not be completed; takes priority over `2` |
| `1` | Command/setup failure, such as invalid input, connection setup or output failure |

Do not treat every nonzero exit as an expired certificate, or an empty alert
table as proof of a complete inventory. Read the problems and any skipped
entries too. A setup failure can stop the command before scan output exists.

The current scan visits the servers listed in `topology.servers` and reads
`*.crt` recursively under `/var/lib/rancher/rke2/server/tls`. It does not open
private keys. Repeated certificates in bundles are deduplicated per node.
Agent-only nodes, custom RKE2 data directories, Gateway-served certificates and
other certificate locations are not covered by this command.

Internal leaf alerts use 150, 100 and 30 days remaining. The renewal-window
finding uses 120 days; CA alerts use 180 days. These are the current tool's
thresholds, not a site's maintenance policy or a guarantee about every RKE2
version. A renewal-window finding does not perform renewal.

## Preserve the scan in a report

Each scan creates a run under `./out/runs/` by default. Use the run ID printed
by **that scan**, rather than relying on whichever run is newest:

```bash
# Replace RUN_ID with the ID printed by certs
malmok report --run RUN_ID
```

The report writes `audit-report.md` and `handoff.json` into the run's
`artifacts/` directory (and DNS records when applicable). It reads the saved
run, not the live cluster. Running `report` later does not refresh expiry data.
If you used `certs --bundle PATH`, pass the same `--bundle PATH` to `report`.

In `handoff.json`, `certificates.measured` means a scan was recorded, **not that
every certificate was readable**. Check `certificates.unmeasured` alongside
`certificates.items`. Items carry node, subject, absolute `notAfter`, days at
scan time and diagnostic code. Older runs without a scan report
`certificates.measured: false`; re-rendering them does not invent measurements.

A scan-only run is not an installation run and can have `run.result: incomplete`
because it has no apply completion event. Use the certificate fields and command
exit status for the scan result, not the installation-result field.

This is a point-in-time check. Scheduling, notifications and renewal decisions
remain with the operator. Keep the configuration and reports protected: they
can contain internal hostnames, addresses and certificate subjects.
