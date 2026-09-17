# Checking the plan against policy

`malmok plan -o json` writes the plan as a document, so a policy engine can
judge it before anything on a node changes.

!!! info "Available on main"

    The `-o json` flag and the `policy/` rules are on current `main`, after
    v0.96.5. They are not in that release's installer download. See the
    [changelog](https://github.com/ryxenix/malmok/blob/main/CHANGELOG.md) and
    [installation](../getting-started/installation.md).

## Why the plan

The plan is the last artefact before the first mutating phase, and it is a pure
function of the document and what the nodes turned out to be. Refusing there
costs a re-run. Refusing later costs a half-built cluster.

It is not the same thing as the document. `cluster.yaml` says what was asked
for; the plan says what will actually be installed, including anything that had
to be downgraded because a node could not deliver it. A rule written against
the document cannot see that difference.

## Run it

```bash
malmok plan -f cluster.yaml -o json | conftest test -
```

In `json` mode stdout carries the plan and nothing else. Everything written for
a person -- the resolved configuration, the preflight findings, the plan table
-- goes to stderr, so the pipe is not contaminated and you still see the run.

The plan needs real nodes. `plan` runs preflight first and refuses to plan
against machines nobody looked at, so this belongs at the site, in front of an
install. It cannot run in CI against a cluster that does not exist yet.

`--validate-only` with `-o json` is refused: there is no plan at that point, and
an empty document would fail in whatever was reading the pipe rather than here.

### Exit status

`plan` exits non-zero when a preflight check blocks the install, when no node
was reached, and when the plan downgrades what the document asked for and
`--approve` was not given. In `json` mode the document is written before that
last check, so a pipeline still receives the plan it was asked to judge even
when the command then refuses to proceed.

## What the rules say

The rules live in `policy/` and are split by the kind of claim they make,
because the two are not the same kind of thing.

**Denied — the plan contradicts itself.** These hold whatever a site has
agreed, so they are the ones worth blocking on:

| Rule | What it catches |
|---|---|
| Unrecorded dataplane change | `actual` differs from `requested` with no downgrade recording it |
| Unrecorded storage change | the same, for the storage driver |
| Downgrade without a trigger | a downgrade naming no probe that caused it |
| Exclusion without a trigger | a node dropped from the cluster with no probe behind it |
| Empty plan | a plan covering no node, which passes every other rule by having nothing to check |

The first two matter because the audit trail is the point. Six months later the
question is why this cluster runs Traefik when the contract said Cilium, and an
unrecorded substitution has no answer to give.

**Warned — a decision belongs to somebody.** A recorded downgrade is not a
defect; making them visible and approvable is what the tool is for. Whether
*this* site accepts one is not something a rule shipped with Malmok can know,
so it says so rather than deciding:

- any downgrade, with the code, the substitution and the reason;
- any node that will not join the cluster.

## Adding your own

Put more `.rego` files in `policy/`. They run in the same `main` package, so a
`deny` you add joins the ones above.

A site rule is usually the opposite shape to the ones here: not "the plan
contradicts itself" but "this contract does not permit that outcome". Pinning a
dataplane is the common one:

```rego
package main

import rego.v1

deny contains msg if {
	input.actual.dataplane != "cilium-gw"
	msg := sprintf("this site requires cilium-gw; the plan installs %q", [input.actual.dataplane])
}
```

## Testing the rules

The rules are code, so they are tested as code:

```bash
conftest verify
```

CI runs this on every push with the conftest version pinned, so a policy
language that changed under the rules fails as a syntax error nobody wrote.

Rego unit tests rather than example plans fed through `conftest test`: a fixture
that is supposed to fail makes the command exit non-zero, so a green check would
have to invert exit codes for some documents and not others.

!!! warning "What CI does not check"

    CI verifies the rules against themselves. It does not run them against a
    generated plan, because a plan is a function of real nodes and CI has none.
    A rule that is internally consistent can still be wrong about a real
    cluster; run the pipe at the site before relying on it.
