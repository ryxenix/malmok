# FAQ and tool comparisons

Malmok is an opinionated RKE2 lifecycle tool. It is not intended to replace
every automation or Kubernetes provisioning system.

## What if the customer has already prepared the servers?

That is where Malmok starts. Servers being available does not mean the RKE2
cluster is installed. Malmok uses SSH to inspect those Linux hosts, show a
build plan, install the cluster, resume interrupted runs and produce a handoff
report. The servers may be customer-provided, manually prepared or provisioned
by automation; no particular provisioning tool is required.

[Start with existing servers →](../getting-started/quick-start.md)

## Does it fit systems integration and public-sector delivery? { #customer-delivery }

It can fit projects where the customer provides Linux servers, RKE2 has already
been selected, and the delivery team must build and hand over the cluster.
Examples include repeatedly delivering AI, document-processing, search or data
platforms to customer sites, including public-sector and state-owned enterprises.
These are intended use cases, not claims of verified deployments in those sectors.

Preflight checks help expose differences between sites; resumable execution helps
with interrupted builds; reports provide records for review and handoff. Malmok
provides the cluster foundation, not the solution's application deployment.

If the customer already has a suitable supported cluster, there may be no reason
to build another one. Check any mandated distribution and support arrangement
before selecting RKE2. Malmok does not establish procurement eligibility, security
certification or acceptance compliance, and does not replace the supplier's
maintenance responsibilities. Its reports are supporting records, not acceptance
certificates. The alpha status and verification limits still apply.

## Could I use Terraform or Ansible instead?

Yes. If you already have reliable RKE2 automation, you do not need to replace
it. Malmok is not an alternative to IaC as a practice: its `cluster.yaml` is
declarative configuration, too. Its purpose is to reduce the RKE2-specific
workflow you need to assemble and maintain yourself.

If the machines and network are already prepared, another provisioning step
may be unnecessary. Configuration and cluster setup still remain. Malmok can
start there, independently of how the infrastructure was prepared.

Use Ansible when you need general configuration management across arbitrary
software and operating-system state.

Malmok owns one narrower workflow:

- validate an RKE2-specific document;
- measure real nodes and peer-to-peer network paths without changing them;
- show the resolved plan before execution;
- resume an interrupted, idempotent phase graph; and
- produce stable diagnostic codes, JSONL events and a handoff report.

The tools can be used together. Provision machines and enforce site-wide policy
with your existing automation, then give Malmok the hosts that answer SSH.

## How is this different from k0sctl?

[k0sctl](https://github.com/k0sproject/k0sctl) is the closest analogue: it
bootstraps and manages k0s clusters from a declarative file and supports
upgrades and air-gap artifacts. If the target distribution is k0s, use it.

Malmok targets RKE2 and provides an opinionated path through host preparation,
Cilium or its conservative fallback, Gateway API, PKI, GitOps and
observability. The same measurement and evidence model is used for online and
air-gapped runs.

## Why not CAPRKE2?

[CAPRKE2](https://caprke2.docs.rancher.com/) is a Cluster API bootstrap and
control-plane provider. If an organization already operates a management
cluster, an infrastructure provider and Cluster API reconciliation, CAPRKE2 is
likely the better fit.

Malmok starts from existing SSH-reachable machines. It runs from an operator's
workstation as a single binary and leaves no management controller or node
agent behind.

## Can I use it in production today?

Malmok is alpha software. Existing successful deployments do not establish
production readiness for every topology or network. Review the
[verification matrix](../40-verification-matrix.md), check the
[air-gap conditions](../guides/air-gap.md) where applicable, and validate your
own environment and recovery procedure before adoption. The `v1alpha1` schema
may change between minor releases.

## Where does Malmok stop?

Malmok does not:

- provision machines, networks or DNS zones;
- change firewall policy;
- create application `HTTPRoute` resources;
- deploy application workloads; or
- replace ongoing alerting and day-2 operational ownership.

That boundary is intentional. Malmok builds and hands over the cluster
foundation; applications and site infrastructure retain their existing owners.
