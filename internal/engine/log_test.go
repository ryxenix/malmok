package engine

import "testing"

// The event schema gives phase, step and node fields of their own
// (docs/11-execute.md §5.1), and its own example writes the step as
// `rke2-server-ready`. Emitting the whole id into that field made every
// rendered line repeat the phase and the node, because a renderer that joins
// three fields cannot know that one of them already contains the other two.
func TestEventStepIsTheBareName(t *testing.T) {
	tests := []struct {
		name, id, phase, node, want string
	}{
		{
			name: "a node step drops both the phase and its own host",
			id:   "l0-node-prep/modules@192.168.88.241", phase: "l0-node-prep",
			node: "192.168.88.241", want: "modules",
		},
		{
			// A cluster-scoped phase names no node, so the host in the id has
			// no field to move to. It is dropped: the node the step happened
			// to talk to is not what the step is about, and a failure says
			// which host it was in its own message.
			name: "a cluster step drops the host it ran on",
			id:   "l2-pki/issuer-ready@192.168.88.241", phase: "l2-pki",
			node: "", want: "issuer-ready",
		},
		{
			name: "an id that is already bare is left alone",
			id:   "ready", phase: "l1-bootstrap", node: "10.0.0.11", want: "ready",
		},
		{
			name: "a host that is not the node is still dropped",
			id:   "l1-join-agent/token@10.0.0.12", phase: "l1-join-agent",
			node: "10.0.0.11", want: "token@10.0.0.12",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventStep(tc.id, tc.phase, tc.node); got != tc.want {
				t.Errorf("eventStep(%q, %q, %q) = %q, want %q",
					tc.id, tc.phase, tc.node, got, tc.want)
			}
		})
	}
}
