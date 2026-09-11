# On-premises deployment guide

A checklist for operators building and handing over RKE2 on company servers or
in a customer's data centre. It assumes Linux servers are ready and RKE2 has
been selected. If a suitable supported cluster already exists, first decide
whether another cluster is needed at all.

The developer used an early Malmok version on real data-centre servers. That
experience informs the tool; it is not a guarantee of operational reliability
or suitability for every customer. Malmok is alpha software. Review the
[verification matrix](../40-verification-matrix.md) and
[configuration results](https://github.com/ryxenix/malmok#what-is-verified).

## 1. Agree on changes and responsibilities

- Identify new servers versus machines with existing services or data. Do not use this workflow to overwrite an existing installation.
- Agree on target nodes, change approval, maintenance window, acceptable interruption and stop conditions.
- Identify infrastructure, network and application contacts and who owns recovery.
- Check backups and restoration procedures for data that must survive. A backup file alone does not prove restoration works.
- Review [host changes](https://github.com/ryxenix/malmok#what-it-changes-on-a-node) before granting access.

## 2. Prepare the environment

| Area | What to confirm on site |
|---|---|
| Hosts and access | Supported OS and architecture, SSH host keys, accounts and working sudo |
| Network | Operator-to-node and peer connectivity, approved firewall policy, address conflicts |
| Registration | Stable DNS or VIP for future nodes and its owner |
| Time and certificates | Reachable NTP, certificate SANs, expiry, trust chain and renewal owner |
| Disks and storage | Space for OS, etcd, images and application data; StorageClass, retention and backups |
| External access | Direct, proxy or disconnected network; image, chart and binary sources |

Required ports depend on node roles and configuration. Preflight measures
connectivity; it does not open firewalls or approve site policy. Check the
verification limits for proxies and external registries and test your environment.

`local-path` uses node-local disk. Do not assume replicated storage or data
availability after a node failure. Site-owned CSI requires separate preparation
and validation. Malmok does not currently install Longhorn or NFS.

For disconnected sites, follow the [air-gap guide](air-gap.md) to prepare RKE2
artifacts, the Malmok platform image bundle, charts and required certificates.
Check versions, checksums and target architectures. Do not assume the bundle
contains your application's images too.

## 3. Check before execution

Use the [wizard](../getting-started/quick-start.md) or a configuration file.
For the file-based workflow:

```bash
# Validate without contacting a node
malmok plan -f cluster.yaml --validate-only

# Inspect nodes and communication paths
malmok preflight -f cluster.yaml

# Review the resolved configuration and plan
malmok plan -f cluster.yaml
```

Resolve blockers and assess warnings and unmeasured items. Passing preflight
does not establish business acceptance or uninterrupted operation. Record the
Malmok version, RKE2 version and final configuration before execution.

## 4. Verify after installation

```bash
# Build from the approved configuration
malmok apply -f cluster.yaml

# Generate the report for the most recent run
malmok report
```

- Confirm intended node counts and roles, Ready nodes, and any failed or pending pods.
- Test DNS, TLS and gateway access from the actual client network.
- Create and mount a test PVC on the selected storage and check persistence after restarting a test workload.
- Verify monitoring collection and required alert delivery separately.
- Test the delivered application's functionality, performance and backup restoration. Cluster health does not replace application acceptance.

!!! warning "A built cluster is not an operations-ready service"

    Application deployment, data protection, incident response, certificate
    renewal and operational ownership still need attention. Malmok does not
    automatically complete or approve them.

## 5. Respond to interruptions

Keep the run ID and diagnostic codes, investigate the cause, then follow the
[resume procedure](upgrade-and-recovery.md). Do not blindly repeat new runs or
delete the state directory.

!!! warning "Resume is not restore or rollback"

    Resume continues an interrupted build toward its target state. It does not
    revert changes or restore lost data. Plan data and etcd backups and
    restoration separately.

## 6. Hand over the result

- Installed versions, nodes and roles, registration address and configuration
- Run IDs, reports, unresolved warnings and excluded verification items
- Image and chart sources without credentials, and the artifact inventory
- Backup locations, schedule, restoration test results and access permissions
- Owners for certificate renewal, monitoring, alerts, upgrades and incident response

Transfer kubeconfigs, tokens and private keys separately through an approved
secure channel. Using `file://` or `env://` references does not make every log
or report safe to publish. Review customer addresses and account information
before sharing.

A report is supporting evidence, not a security certificate or an acceptance
approval. Supplier maintenance obligations and customer approval procedures
remain separate.
