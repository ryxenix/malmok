// Package codes is the single source of truth for platformctl's diagnostic
// codes: preflight probes (PF), post-apply verification (PV), upgrade
// preconditions (UP), execution failures (EX), maintenance checks (MC) and
// downgrade reasons (DG).
//
// WHY THIS IS CODE AND NOT A MARKDOWN TABLE
//
//	A hand-maintained registry document drifts from the implementation within
//	weeks, and a code that means one thing in the audit report and another in
//	the engine is worse than no code at all. Definitions live here; the markdown
//	registry under docs/ is a `go generate` artifact and must not be edited.
//
// Codes are stable identifiers. They are never translated (PF-204 is PF-204 in
// every locale) and they are never recycled: retiring a code means leaving its
// number unused, because audit reports and customer tickets outlive releases.
//
// Summary and Message are English by policy — see CLAUDE.md. Event log detail
// written in Korean breaks grep and issue-tracker search.
package codes

//go:generate go run ./gen -out ../../docs/99-codes.md

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Family
// ---------------------------------------------------------------------------

// Family is the prefix that scopes a code's number space. Numbers are unique
// across the whole registry, not merely within a family, so that a bare code in
// a log line is unambiguous.
type Family string

const (
	// FamilyPreflight covers read-only measurement performed before any change
	// is made to a node. See docs/10-preflight-plan.md.
	FamilyPreflight Family = "PF"
	// FamilyVerification covers checks performed against the running system
	// after apply — on the wire, not on the input files. See docs/20-cert.md.
	FamilyVerification Family = "PV"
	// FamilyMaintenance covers periodic day-2 inspection items. See
	// docs/30-maintenance.md.
	FamilyMaintenance Family = "MC"
	// FamilyDowngrade covers reasons a requested configuration was replaced by
	// a lesser one. See docs/10-preflight-plan.md.
	FamilyDowngrade Family = "DG"
	// FamilyExecution covers failures of the phase runner itself. See
	// docs/11-execute.md §6 and internal/codes/execution.go.
	FamilyExecution Family = "EX"
	// FamilyUpgrade covers the preconditions of moving an existing cluster to a
	// new version. Measured before anything is touched, like a preflight check,
	// but answering a different question: a PF code says whether an install
	// will work here, a UP code says whether this step is legal from where the
	// cluster already is. See internal/codes/upgrade.go.
	FamilyUpgrade Family = "UP"
)

// Families lists every family in registry display order.
var Families = []Family{FamilyPreflight, FamilyVerification, FamilyUpgrade, FamilyExecution, FamilyMaintenance, FamilyDowngrade}

func (f Family) String() string { return string(f) }

// ---------------------------------------------------------------------------
// Severity
// ---------------------------------------------------------------------------

// Severity drives what the plan generator or the runner does when a code fires.
// It applies to PF, PV and EX only.
//
// MC codes carry no severity: a maintenance check is graded at runtime against
// thresholds agreed with the customer (docs/30-maintenance.md §4.3), so baking
// a severity into the definition would hardcode one site's contract.
//
// DG codes carry no severity either: they record why a downgrade happened, and
// the decision to accept it belongs to DowngradePolicy.
type Severity string

const (
	// SeverityBlock stops plan generation. There is no safe way forward.
	SeverityBlock Severity = "block"
	// SeverityDegrade means the requested configuration is unavailable but a
	// lesser one exists. DowngradePolicy decides what happens next.
	SeverityDegrade Severity = "degrade"
	// SeverityWarn allows the install to proceed but records an operational
	// risk in the audit report.
	SeverityWarn Severity = "warn"
	// SeverityInfo is collected evidence with no verdict attached.
	SeverityInfo Severity = "info"
)

var validSeverities = map[Severity]bool{
	SeverityBlock:   true,
	SeverityDegrade: true,
	SeverityWarn:    true,
	SeverityInfo:    true,
}

func (s Severity) String() string { return string(s) }

// Valid reports whether s is one of the four defined severities.
func (s Severity) Valid() bool { return validSeverities[s] }

// ---------------------------------------------------------------------------
// Code
// ---------------------------------------------------------------------------

// Code is one registry entry. Everything a probe implementation, the audit
// report and the generated markdown need lives here.
type Code struct {
	// ID is the stable identifier, e.g. "PF-204". Format: <family>-<3 digits>.
	ID string

	// Family is derived from the ID prefix and is checked against it by
	// Validate; it is stored explicitly so callers can filter without parsing.
	Family Family

	// Category is the human-facing grouping used as a section heading in the
	// generated registry, e.g. "eBPF / dataplane capability".
	Category string

	// Summary is a short English label — what the code checks or reports.
	// Used as the table row in the generated registry.
	Summary string

	// Message is the default English text emitted as the JSONL `detail` field
	// when the code fires. Probe implementations may replace it with something
	// more specific, but never with a translated string.
	Message string

	// Severity is set for PF, PV and EX; empty for MC and DG. See Severity.
	Severity Severity

	// Reasons are sub-codes that distinguish failure modes within one code,
	// e.g. PF-204 fails as EBPF_LOAD_DENIED or EBPF_VERIFIER_REJECT. They are
	// carried in ProbeResult.Code and appear in the audit report, so the
	// operator can tell "the kernel refuses eBPF" from "we lack permission".
	Reasons []string
}

// HasSeverity reports whether this code's family carries a severity.
func (c Code) HasSeverity() bool {
	return c.Family == FamilyPreflight ||
		c.Family == FamilyVerification ||
		c.Family == FamilyUpgrade ||
		c.Family == FamilyExecution
}

func (c Code) String() string {
	if c.Severity != "" {
		return fmt.Sprintf("%s [%s] %s", c.ID, c.Severity, c.Summary)
	}
	return fmt.Sprintf("%s %s", c.ID, c.Summary)
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// sources holds every family's slice. Lookups go through the map built in
// init, but All and Validate walk these slices instead, so a duplicate ID
// cannot silently vanish behind a map overwrite.
var sources [][]Code

// registry indexes sources by ID. On a duplicate the FIRST definition wins and
// the collision is recorded in initErrs — init does not panic, because a test
// reporting "PF-204 defined twice" is far more useful than a package that
// refuses to load.
var registry = map[string]Code{}

var initErrs []error

func init() {
	sources = [][]Code{
		preflightCodes,
		verificationCodes,
		upgradeCodes,
		executionCodes,
		maintenanceCodes,
		downgradeCodes,
	}
	for _, group := range sources {
		for _, c := range group {
			if prev, dup := registry[c.ID]; dup {
				initErrs = append(initErrs, fmt.Errorf(
					"duplicate code %s: %q and %q", c.ID, prev.Summary, c.Summary))
				continue
			}
			registry[c.ID] = c
		}
	}
}

// Lookup returns the code registered under id.
func Lookup(id string) (Code, bool) {
	c, ok := registry[id]
	return c, ok
}

// MustLookup returns the code registered under id and panics if it is absent.
// Intended for package-level initialisation in probe implementations, where an
// unknown code is a programming error that should never reach a customer site.
func MustLookup(id string) Code {
	c, ok := registry[id]
	if !ok {
		panic("codes: unknown code " + id)
	}
	return c
}

// All returns every registered code sorted by family order, then by ID.
func All() []Code {
	out := make([]Code, 0, len(registry))
	for _, group := range sources {
		out = append(out, group...)
	}
	sortCodes(out)
	return out
}

// ByFamily returns the codes belonging to f, sorted by ID.
func ByFamily(f Family) []Code {
	var out []Code
	for _, c := range All() {
		if c.Family == f {
			out = append(out, c)
		}
	}
	return out
}

// Categories returns f's category names in first-appearance order, which is the
// order the source file declares them and therefore the order the generated
// registry should print them.
func Categories(f Family) []string {
	var out []string
	seen := map[string]bool{}
	for _, group := range sources {
		for _, c := range group {
			if c.Family != f || seen[c.Category] {
				continue
			}
			seen[c.Category] = true
			out = append(out, c.Category)
		}
	}
	return out
}

func familyRank(f Family) int {
	for i, known := range Families {
		if known == f {
			return i
		}
	}
	return len(Families)
}

func sortCodes(cs []Code) {
	sort.SliceStable(cs, func(i, j int) bool {
		ri, rj := familyRank(cs[i].Family), familyRank(cs[j].Family)
		if ri != rj {
			return ri < rj
		}
		return cs[i].ID < cs[j].ID
	})
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

var idPattern = regexp.MustCompile(`^(PF|PV|UP|EX|MC|DG)-[0-9]{3}$`)

// Validate returns every structural problem in the registry. The test suite
// fails on a non-empty result; nothing else calls it at runtime.
//
// This is where number collisions are caught. CLAUDE.md requires that new codes
// be added here before use, and that collisions be prevented by test rather
// than by convention.
func Validate() []error {
	errs := append([]error(nil), initErrs...)

	for _, c := range All() {
		switch {
		case !idPattern.MatchString(c.ID):
			errs = append(errs, fmt.Errorf("%s: malformed ID, want <family>-<3 digits>", c.ID))
		case Family(strings.SplitN(c.ID, "-", 2)[0]) != c.Family:
			errs = append(errs, fmt.Errorf("%s: ID prefix does not match Family %q", c.ID, c.Family))
		}

		if familyRank(c.Family) == len(Families) {
			errs = append(errs, fmt.Errorf("%s: unknown family %q", c.ID, c.Family))
		}
		if strings.TrimSpace(c.Category) == "" {
			errs = append(errs, fmt.Errorf("%s: empty Category", c.ID))
		}
		if strings.TrimSpace(c.Summary) == "" {
			errs = append(errs, fmt.Errorf("%s: empty Summary", c.ID))
		}
		if strings.TrimSpace(c.Message) == "" {
			errs = append(errs, fmt.Errorf("%s: empty Message", c.ID))
		}

		switch {
		case c.HasSeverity() && !c.Severity.Valid():
			errs = append(errs, fmt.Errorf("%s: %s codes require a valid severity, got %q",
				c.ID, c.Family, c.Severity))
		case !c.HasSeverity() && c.Severity != "":
			errs = append(errs, fmt.Errorf("%s: %s codes must not carry a severity, got %q",
				c.ID, c.Family, c.Severity))
		}

		for _, r := range c.Reasons {
			if r != strings.ToUpper(r) || strings.TrimSpace(r) == "" {
				errs = append(errs, fmt.Errorf("%s: reason %q must be non-empty UPPER_SNAKE_CASE", c.ID, r))
			}
		}
	}
	return errs
}
