# Contributing

Malmok installs software as root on machines people run production on. That
shapes what a good change looks like here more than any style rule does.

## Before you open a pull request

Open an issue first. The repository is moving quickly under a single author,
and agreeing on user-visible behavior before the code is written avoids a
large rewrite during review.

## What CI checks

```bash
go build ./...
go vet ./...
go test -race ./...
gofmt -l .                      # must be empty
go generate ./internal/codes/   # docs/99-codes.md must not change
```

The lab suite is behind a `lab` build tag because it **wipes real machines**.
Nothing in CI runs it, and you should only run it against hardware you are
willing to lose:

```bash
export MALMOK_LAB_SERVER=192.0.2.41 MALMOK_LAB_AGENT=192.0.2.44
NODE_PASSWORD=... scripts/matrix.sh
```

## Rules that are not negotiable

These are compatibility and safety constraints, not style preferences.

- **`internal/engine` must not import `internal/tui`.** The engine emits JSONL
  events; the TUI draws them (ADR-002). A test enforces this, and CI runs it.
- **Preflight does not change anything.** It measures. A probe that fixes what
  it finds is a bug, however convenient.
- **Every phase is idempotent and resumable.** A phase that only works on a
  clean machine does not survive contact with a real one.
- **`plan` is a pure function.** `(ClusterSpec, []NodeCapability) -> Plan`, no
  network, no side effects.
- **No plaintext secrets in `cluster.yaml`.** References only.
- **This tool does not create HTTPRoutes** and does not install ingress-nginx.
  Application charts own routes; Malmok stops at the Gateway API boundary.

## Observe the thing itself

Most defects found in this project so far had the same shape: something
inferred a state instead of measuring it. The port check decided a port was
free by how a connection was refused, on a network that swallowed the refusal.
A step reported "waiting" while the condition it waited for could never
become true.

So: check the listener, not the error code. Read the node's own answer, not a
proxy for it. If the tool must wait, it must also be able to conclude that
waiting is pointless.

## Diagnostic codes

New failure modes get a code. Define it in `internal/codes/` first, then use
it; `docs/99-codes.md` is generated and must not be hand-edited. Retired
numbers are never reused -- audit reports and customer tickets outlive
releases.

## Commits

Conventional prefixes (`feat`, `fix`, `docs`, `refactor`, `chore`), a scope,
and a body that says **why**. Comments and commit messages are in English;
logs, events and error codes are English too, because Korean log lines break
grep and issue search.

## Security

Do not report vulnerabilities in an issue. See [SECURITY.md](SECURITY.md).
