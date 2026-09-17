# Tests for the plan policy, run by `conftest verify`.
#
# Rego tests rather than fixture files fed through `conftest test`: a fixture
# that is supposed to fail makes the command exit non-zero, so a green CI step
# would have to invert exit codes for some inputs and not others. Testing the
# rules directly says what each one is for and keeps the CI step a plain pass.
package main

import rego.v1

# A plan that asked for cilium-gw and got it, on two nodes, with nothing
# downgraded and nobody excluded.
clean := {
	"requested": {"dataplane": "cilium-gw", "storage": "local-path"},
	"actual": {"dataplane": "cilium-gw", "storage": "local-path"},
	"nodes": ["10.10.0.11", "10.10.0.21"],
}

test_clean_plan_is_allowed if {
	count(deny) == 0 with input as clean
	count(warn) == 0 with input as clean
}

# The contradiction the first rule exists for: the dataplane moved and nothing
# in the document says why.
test_unrecorded_dataplane_change_is_denied if {
	d := deny with input as object.union(clean, {"actual": {
		"dataplane": "canal-traefik",
		"storage": "local-path",
	}})
	count(d) == 1
}

test_unrecorded_storage_change_is_denied if {
	d := deny with input as object.union(clean, {"actual": {
		"dataplane": "cilium-gw",
		"storage": "longhorn",
	}})
	count(d) == 1
}

# The same change, recorded. This is the ordinary path and must stay allowed --
# a downgrade is visible, not forbidden.
recorded := object.union(clean, {
	"actual": {"dataplane": "canal-traefik", "storage": "local-path"},
	"downgrades": [{
		"code": "DG-001",
		"from": "cilium-gw",
		"to": "canal-traefik",
		"triggeredBy": ["PF-204"],
		"nodes": ["10.10.0.21"],
		"detail": "the kernel refused to load an eBPF program",
	}],
})

test_recorded_downgrade_is_not_denied if {
	count(deny) == 0 with input as recorded
}

# But it is surfaced, because accepting it is somebody's decision to make.
test_recorded_downgrade_warns if {
	count(warn) == 1 with input as recorded
}

# A downgrade with no probe behind it decided on assumption.
test_downgrade_without_a_trigger_is_denied if {
	d := deny with input as object.union(recorded, {"downgrades": [{
		"code": "DG-001",
		"from": "cilium-gw",
		"to": "canal-traefik",
		"triggeredBy": [],
		"nodes": ["10.10.0.21"],
		"detail": "no reason recorded",
	}]})
	count(d) == 1
}

test_exclusion_without_a_trigger_is_denied if {
	d := deny with input as object.union(clean, {"excluded": [{
		"node": "10.10.0.21",
		"triggeredBy": [],
		"detail": "no reason recorded",
	}]})

	# One deny for the missing trigger; the exclusion also warns, which is a
	# different rule and not counted here.
	count(d) == 1
}

test_excluded_node_warns if {
	w := warn with input as object.union(clean, {"excluded": [{
		"node": "10.10.0.21",
		"triggeredBy": ["PF-204"],
		"detail": "the kernel refused to load an eBPF program",
	}]})
	count(w) == 1
}

# A plan covering nothing passes every other rule by having nothing to check.
test_empty_plan_is_denied if {
	d := deny with input as {
		"requested": {"dataplane": "cilium-gw", "storage": "local-path"},
		"actual": {"dataplane": "cilium-gw", "storage": "local-path"},
		"nodes": [],
	}
	count(d) == 1
}
