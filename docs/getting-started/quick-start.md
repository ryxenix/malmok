# Quick start

This guide builds a single-server RKE2 cluster from an existing machine that
answers SSH. Malmok does not provision the machine or modify its firewall.

## Before you begin

You need:

- a Linux or macOS operator machine on amd64 or arm64;
- an Ubuntu 22.04/24.04 or Rocky 9 target node;
- SSH and root access, or working privilege escalation, on that node; and
- at least 20 GB of free disk space on the node. Two CPU cores, 4 GB RAM and
  50 GB free disk are recommended.

The nodes must be able to reach each other on the ports reported by preflight,
including 6443, 9345, 2379–2380 and 10250 where their roles require them.

## Install Malmok and open the wizard

You do not need an existing `cluster.yaml`.

```bash
# Install on your operator machine; skip if already installed
curl -fsSL https://malmok.dev/install.sh | sh

# Configure, check and install interactively
malmok apply --tui
```

Enter your node addresses and SSH access details in the wizard. It creates
the configuration file and guides the build using the same engine as the CLI.
Use `--lang ko` for Korean. To explore without contacting nodes, run
`malmok apply --demo` after installing Malmok.

For manual downloads or restricted networks, see [installation options](installation.md).

## Alternative: use a configuration file

Choose this path for repeatable or automated runs. It is not a prerequisite
for the wizard, and you do not need to install again after completing the wizard.

### 1. Describe the cluster

Save this file as `cluster.yaml`. Addresses in `192.0.2.0/24` are reserved for
documentation; replace them before running anything.

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

!!! warning "Choose a stable registration address before growing"

    This example accepts a single server's address as the registration
    endpoint. Before adding control-plane servers, introduce stable DNS or a
    virtual IP; otherwise existing nodes must be rejoined when it changes.

### 2. Validate without touching a node

```bash
malmok plan -f cluster.yaml --validate-only
```

Unknown fields and invalid combinations are reported before SSH is attempted.

### 3. Measure without changing

```bash
malmok preflight -f cluster.yaml
```

Preflight inspects the hosts and runs real network probes. It reports blockers
and recommendations but does not repair or configure the nodes.

### 4. Review the resolved plan

```bash
malmok plan -f cluster.yaml
```

The output includes values supplied by the selected profile and the source of
each value.

### 5. Apply and inspect

```bash
malmok apply -f cluster.yaml
malmok report
```

Every run writes a state directory and JSONL event stream. If the process is
interrupted, use the run identifier printed by Malmok:

```bash
malmok apply --resume RUN_ID
```

