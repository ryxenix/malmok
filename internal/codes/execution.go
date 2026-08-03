package codes

// Execution codes (EX-xxx). Emitted by the phase runner, not by a probe.
//
// WHY THIS FAMILY EXISTS
//
//	docs/11-execute.md §6 requires every failure event to carry a code: without
//	one the failure cannot be traced in the audit report and cannot be
//	translated by a renderer. The four original families do not cover execution
//	— PF measures before any change, PV verifies the wire afterwards, MC is
//	day-2 inspection, DG records a downgrade. "The step ran and did not take"
//	belongs to none of them.
//
// These carry a severity like PF and PV, because they are pass/fail outcomes
// the plan and the operator act on.
//
// A step that knows a better code should supply it: EX-002 means "this failed
// and nobody said why more precisely".

const (
	catExecStep  = "Step execution"
	catExecPhase = "Phase and run control"
)

var executionCodes = []Code{
	// --- EX-0xx · Step execution ------------------------------------------
	{
		ID: "EX-001", Family: FamilyExecution, Category: catExecStep,
		Summary: "Step observation failed",
		Message: "Could not observe the step's current state; the target state is unknown",
		// Worse than a failed apply. Without observation there is no way to
		// decide anything about this step, including whether resuming is safe
		// (§3.2, §4.2 rule 4).
		Severity: SeverityBlock,
	},
	{
		ID: "EX-002", Family: FamilyExecution, Category: catExecStep,
		Summary:  "Step application failed",
		Message:  "The step ran and returned an error",
		Severity: SeverityBlock,
	},
	{
		ID: "EX-003", Family: FamilyExecution, Category: catExecStep,
		Summary: "Target state not reached after applying",
		Message: "The step reported success but re-observation shows the target state was not reached",
		// §3.2 rule 3: an exit code is not evidence of the target state. This
		// code exists so that "the command said it worked" is distinguishable
		// from "the command failed" in the audit report.
		Severity: SeverityBlock,
	},
	{
		ID: "EX-004", Family: FamilyExecution, Category: catExecStep,
		Summary:  "Retry budget exhausted",
		Message:  "The step failed on every attempt allowed by its retry budget",
		Severity: SeverityBlock,
	},
	{
		ID: "EX-005", Family: FamilyExecution, Category: catExecStep,
		Summary: "One-shot step needs explicit confirmation",
		Message: "A one-shot step was left in a non-successful state; re-running it is not automatically safe",
		// §3.3 and §4.2: the runner refuses to decide this on its own.
		Severity: SeverityBlock,
	},

	// --- EX-1xx · Phase and run control ------------------------------------
	{
		ID: "EX-101", Family: FamilyExecution, Category: catExecPhase,
		Summary:  "Phase halted by a failed step",
		Message:  "The phase stopped because one of its steps failed",
		Severity: SeverityBlock,
	},
	{
		ID: "EX-102", Family: FamilyExecution, Category: catExecPhase,
		Summary: "Node traversal halted",
		Message: "A node failed, so the remaining nodes in this phase were not attempted",
		// §4.3: a cluster half on the new configuration is harder to diagnose
		// and harder to decide about than one that stopped where it broke.
		Severity: SeverityBlock,
	},
	{
		ID: "EX-103", Family: FamilyExecution, Category: catExecPhase,
		Summary:  "Run halted by a failed phase",
		Message:  "The run stopped; later phases were not entered",
		Severity: SeverityBlock,
	},
	{
		ID: "EX-104", Family: FamilyExecution, Category: catExecPhase,
		Summary:  "Run cancelled",
		Message:  "The run was cancelled before completing",
		Severity: SeverityWarn,
	},
}
