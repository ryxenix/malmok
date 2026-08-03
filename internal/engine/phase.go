package engine

// Grade is how disruptive a phase is. See docs/00-architecture.md §4.2.
//
// It is carried here so the runner can surface it before doing anything: an
// operator approving a plan needs to know which parts restart workloads, and
// "CIS profile" being disruptive rather than additive is exactly the kind of
// thing that must not be discovered during a maintenance window.
type Grade string

const (
	// GradeAdditive leaves running workloads alone.
	GradeAdditive Grade = "additive"
	// GradeMutating restarts something; brief interruption.
	GradeMutating Grade = "mutating"
	// GradeDisruptive is a reconfiguration, not an addition.
	GradeDisruptive Grade = "disruptive"
)

// Traversal is how a phase visits nodes.
type Traversal string

const (
	// TraversalCluster has no per-node work.
	TraversalCluster Traversal = "cluster"

	// TraversalParallel visits every node at once. Correct for independent
	// per-node preparation such as l0-node-prep.
	TraversalParallel Traversal = "parallel"

	// TraversalSequential visits one node at a time and stops at the first
	// failure. Required wherever a restart is involved: CLAUDE.md forbids
	// restarting every node at once, and §4.3 forbids continuing past a node
	// that failed, because a cluster half on the new configuration is harder to
	// diagnose and harder to decide about than one that stopped where it broke.
	TraversalSequential Traversal = "sequential"
)

// Phase is one entry of the docs/11-execute.md §2 catalogue.
type Phase struct {
	ID    string
	Grade Grade

	Traversal Traversal

	// Nodes is the traversal order for node-scoped phases, ignored otherwise.
	Nodes []string

	// Steps returns the work for one node, or for the cluster when node is
	// empty. It is a function rather than a slice so a phase can vary its work
	// by node — an agent and a server do not run the same steps.
	Steps func(node string) []Step
}

// nodeTargets returns the scopes this phase iterates: a single empty string for
// cluster-scoped phases, otherwise the node list.
func (p Phase) nodeTargets() []string {
	if p.Traversal == TraversalCluster || len(p.Nodes) == 0 {
		return []string{""}
	}
	return p.Nodes
}

func (p Phase) stepsFor(node string) []Step {
	if p.Steps == nil {
		return nil
	}
	return p.Steps(node)
}
