# Security Policy

Malmok holds SSH credentials, writes kubeconfigs, and installs software with
root on machines that people run production on. A vulnerability here is not
a bug in a web page; treat it accordingly.

## Reporting a vulnerability

**Do not open a public issue.**

Report privately through GitHub's advisory form:

  https://github.com/ryxenix/malmok/security/advisories/new

Include what you can:

- the version (`malmok --version`) and how it was installed
- what the tool was asked to do -- the relevant part of `cluster.yaml`, with
  secrets removed
- what happened, and what an attacker gains from it
- a reproduction, even a rough one

You will get a first response within 7 days. If a fix is warranted, expect a
patch release and a published advisory; you will be credited unless you ask
otherwise.

## Supported versions

Malmok is alpha and released from `main`. Only the newest release receives
fixes. There is no long-term support branch yet, and an alpha tool should not
be assumed to have one.

## What counts

In scope:

- credential handling -- SSH keys, node tokens, kubeconfigs, registry logins
  leaking into logs, event streams, audit reports, handoff documents, or run
  directories
- command injection through values that come out of `cluster.yaml`, node
  output, or a chart's response
- host key verification being bypassed outside `--insecure-host-key`
- a cluster left in a materially less secure state than the document asked
  for, without saying so

Out of scope:

- vulnerabilities in RKE2, Cilium, Helm, ArgoCD or cert-manager themselves.
  Report those upstream; if Malmok installs an affected version by default,
  that part is ours and worth telling us about.
- `--insecure-host-key` accepting any host key. That is what the flag says it
  does, and it exists because a freshly imaged node has no known_hosts entry.
- anything requiring an attacker who already has root on the control-plane
  node.

## Notes for operators

- `cluster.yaml` takes secret *references* (`file://`, `env://`),
  not literal values. `--allow-literal-secrets` turns that guard off; a
  document that has been handed to a customer should never need it.
- Run directories under `out/runs/` contain the event stream and the audit
  report. They are written for humans to read afterwards and are not
  encrypted -- treat them as you would treat any operational log.
- Malmok binds real listeners during preflight to measure reachability. They
  are closed when the check ends.
