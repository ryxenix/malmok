# Policy over `malmok plan -o json`.
#
# The plan is the last artefact before anything on a node changes, and it is a
# pure function of the document and what the nodes turned out to be. That makes
# it the right thing to hold a site's rules against: refusing here costs a
# re-run, and refusing later costs a half-built cluster.
#
# Two kinds of rule live here and they are not the same kind of thing.
#
# The deny rules are about the plan contradicting itself -- it changed
# something and did not say why, or it recorded a decision with no evidence
# behind it. Those are defects in the plan whatever a site's policy is, and
# they are the ones worth blocking on.
#
# The warn rules are site preferences. A downgrade is not a defect: the tool
# exists to make them visible and approvable. Whether this particular site
# accepts one is not something a policy file in this repository can know, so it
# says so rather than deciding.
#
#   malmok plan -f cluster.yaml -o json | conftest test -
package main

import rego.v1

# --- contradictions ---------------------------------------------------------

# A dataplane that changed with nothing recording the change. The audit trail
# is the whole point: six months later the question is why this cluster runs
# Traefik when the contract said Cilium, and an unrecorded substitution has no
# answer to give.
deny contains msg if {
	input.actual.dataplane != input.requested.dataplane
	count(object.get(input, "downgrades", [])) == 0
	msg := sprintf(
		"the dataplane was changed from %q to %q and no downgrade records it",
		[input.requested.dataplane, input.actual.dataplane],
	)
}

deny contains msg if {
	input.actual.storage != input.requested.storage
	count(object.get(input, "downgrades", [])) == 0
	msg := sprintf(
		"the storage driver was changed from %q to %q and no downgrade records it",
		[input.requested.storage, input.actual.storage],
	)
}

# A downgrade with no probe behind it is a decision with no reason. The tool
# derives downgrades from measurements; one arriving without its trigger means
# something decided on assumption.
deny contains msg if {
	some d in object.get(input, "downgrades", [])
	count(object.get(d, "triggeredBy", [])) == 0
	msg := sprintf("downgrade %s (%s -> %s) names no probe that triggered it", [d.code, d.from, d.to])
}

# Same for a node dropped from the cluster. Excluding a machine is the most
# consequential thing a plan can decide on its own.
deny contains msg if {
	some e in object.get(input, "excluded", [])
	count(object.get(e, "triggeredBy", [])) == 0
	msg := sprintf("node %s is excluded and names no probe that triggered it", [e.node])
}

# A plan with no nodes is not a plan. It passes every other rule here by having
# nothing to check, which is exactly why it needs its own.
deny contains msg if {
	count(object.get(input, "nodes", [])) == 0
	msg := "the plan covers no node"
}

# --- site preferences -------------------------------------------------------

# Surfaced, not decided. Whether a downgrade is acceptable belongs to whoever
# owns the contract; a policy file shipped with the tool cannot know, and
# guessing would make the answer look agreed when it was assumed.
warn contains msg if {
	some d in object.get(input, "downgrades", [])
	msg := sprintf(
		"%s: %s -> %s (%s). Accept it deliberately or pin the requirement",
		[d.code, d.from, d.to, d.detail],
	)
}

warn contains msg if {
	some e in object.get(input, "excluded", [])
	msg := sprintf("node %s will not join the cluster: %s", [e.node, e.detail])
}
