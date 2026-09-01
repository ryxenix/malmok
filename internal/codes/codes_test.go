package codes

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestRegistryValid is the collision guard CLAUDE.md requires: number clashes
// are prevented by test, not by convention.
func TestRegistryValid(t *testing.T) {
	for _, err := range Validate() {
		t.Errorf("registry invalid: %v", err)
	}
}

// TestPreflightInventory pins the exact PF set. Collecting these codes was a
// one-time archaeology pass over three documents; without an explicit
// inventory, a later edit can drop one and nothing notices until a probe
// references a code that no longer exists.
func TestPreflightInventory(t *testing.T) {
	want := []string{
		// base
		"PF-101", "PF-102", "PF-103", "PF-104", "PF-105", "PF-106", "PF-107", "PF-108", "PF-109",
		// eBPF / dataplane
		"PF-201", "PF-202", "PF-203", "PF-204", "PF-205", "PF-206", "PF-207", "PF-208", "PF-209",
		// security
		"PF-301", "PF-302", "PF-303", "PF-304", "PF-305",
		// storage
		"PF-401", "PF-402", "PF-403", "PF-404", "PF-405", "PF-406", "PF-407",
		// time
		"PF-501", "PF-502", "PF-503", "PF-504",
		// network
		"PF-601", "PF-602", "PF-603", "PF-604", "PF-605", "PF-606",
		"PF-607", "PF-608", "PF-609", "PF-610", "PF-611", "PF-612",
		// registry / PKI
		"PF-701", "PF-702", "PF-703", "PF-704", "PF-705", "PF-706", "PF-707", "PF-708", "PF-709",
		// residue
		"PF-801", "PF-802", "PF-803", "PF-804", "PF-805", "PF-806",
		// certificate material
		"PF-901", "PF-902", "PF-903", "PF-904", "PF-905", "PF-906",
		"PF-910", "PF-911", "PF-912",
	}

	var got []string
	for _, c := range ByFamily(FamilyPreflight) {
		got = append(got, c.ID)
	}
	assertSameSet(t, "PF", want, got)
}

// TestOtherInventories pins the remaining three families for the same reason
// as TestPreflightInventory.
func TestOtherInventories(t *testing.T) {
	tests := []struct {
		family Family
		want   []string
	}{
		{
			family: FamilyVerification,
			want: []string{
				"PV-001", "PV-002", "PV-003", "PV-004",
				"PV-005", "PV-006", "PV-007", "PV-008",
			},
		},
		{
			family: FamilyMaintenance,
			want: []string{
				// certificates: series A, B, C, D
				"MC-101", "MC-102", "MC-111", "MC-112", "MC-113", "MC-121", "MC-131",
				// cluster state
				"MC-201", "MC-202", "MC-203", "MC-204", "MC-205", "MC-206",
				// etcd
				"MC-301", "MC-302", "MC-303", "MC-304", "MC-305", "MC-306",
				// resources / storage
				"MC-401", "MC-402", "MC-403", "MC-404", "MC-405", "MC-406", "MC-407",
				// network / access
				"MC-501", "MC-502", "MC-503", "MC-504", "MC-505", "MC-506",
				// workload / service
				"MC-601", "MC-602", "MC-603", "MC-604", "MC-605",
				// security / configuration
				"MC-701", "MC-702", "MC-703", "MC-704", "MC-705",
			},
		},
		{
			family: FamilyDowngrade,
			want:   []string{"DG-001", "DG-002", "DG-003", "DG-010", "DG-020"},
		},
		{
			family: FamilyUpgrade,
			want: []string{
				"UP-001", "UP-002", "UP-003", "UP-004", "UP-005",
				"UP-101", "UP-102", "UP-103",
			},
		},
		{
			family: FamilyExecution,
			want: []string{
				"EX-001", "EX-002", "EX-003", "EX-004", "EX-005",
				"EX-101", "EX-102", "EX-103", "EX-104",
			},
		},
	}

	for _, tc := range tests {
		t.Run(string(tc.family), func(t *testing.T) {
			var got []string
			for _, c := range ByFamily(tc.family) {
				got = append(got, c.ID)
			}
			assertSameSet(t, string(tc.family), tc.want, got)
		})
	}
}

// TestTotalInventory pins the overall count. The collection pass over the three
// design documents found 121 defined codes; PF-908 was referenced without a
// definition and became PF-612, the EX family added 9 for the phase runner,
// and PF-109 (machine UUID) arrived with the handoff.
func TestTotalInventory(t *testing.T) {
	const want = 142
	if got := len(All()); got != want {
		t.Errorf("registry holds %d codes, want %d", got, want)
	}
}

// TestNoDanglingReferences walks the repository and fails on any code cited in
// Go source or example YAML that the registry does not define. This is what
// surfaced PF-908, which the schema referenced for a while with no definition
// anywhere in the documents.
func TestNoDanglingReferences(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	pattern := regexp.MustCompile(`\b(?:PF|PV|EX|MC|DG)-[0-9]{3}\b`)

	// The codes package is skipped deliberately: it is the source of truth, and
	// its comments legitimately discuss retired numbers such as PF-908.
	skip := filepath.Join(root, "internal", "codes")

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || path == skip {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".yaml", ".yml":
		default:
			return nil
		}
		// Test files are exempt. A negative test has to name an unregistered
		// code to prove the rejection path works -- internal/event does exactly
		// that with PF-908 and PF-999. What this test guards against is
		// production code citing a code that does not exist, which is how
		// PF-908 lived in api/v1alpha1 for a release.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, id := range pattern.FindAllString(string(body), -1) {
			if _, ok := Lookup(id); !ok {
				t.Errorf("%s references %s, which is not in the registry", rel, id)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestSeverityByFamily(t *testing.T) {
	tests := []struct {
		family      Family
		wantSetting bool
	}{
		{FamilyPreflight, true},
		{FamilyVerification, true},
		{FamilyExecution, true},
		{FamilyUpgrade, true},
		{FamilyMaintenance, false},
		{FamilyDowngrade, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.family), func(t *testing.T) {
			for _, c := range ByFamily(tc.family) {
				got := c.Severity != ""
				if got != tc.wantSetting {
					t.Errorf("%s: severity present = %v, want %v (severity=%q)",
						c.ID, got, tc.wantSetting, c.Severity)
				}
			}
		})
	}
}

func TestLookup(t *testing.T) {
	tests := []struct {
		id           string
		wantFound    bool
		wantSeverity Severity
		wantReason   string
	}{
		{id: "PF-204", wantFound: true, wantSeverity: SeverityDegrade, wantReason: "EBPF_LOAD_DENIED"},
		{id: "PF-501", wantFound: true, wantSeverity: SeverityBlock},
		{id: "PF-612", wantFound: true, wantSeverity: SeverityWarn},
		{id: "PF-103", wantFound: true, wantSeverity: SeverityInfo},
		// Retired: renumbered to PF-612. Must never come back.
		{id: "PF-908", wantFound: false},
		{id: "PF-000", wantFound: false},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			c, ok := Lookup(tc.id)
			if ok != tc.wantFound {
				t.Fatalf("found = %v, want %v", ok, tc.wantFound)
			}
			if !ok {
				return
			}
			if c.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", c.Severity, tc.wantSeverity)
			}
			if tc.wantReason != "" && !contains(c.Reasons, tc.wantReason) {
				t.Errorf("reasons %v missing %q", c.Reasons, tc.wantReason)
			}
		})
	}
}

func TestMustLookupPanicsOnUnknown(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustLookup did not panic on an unknown code")
		}
	}()
	MustLookup("PF-000")
}

func TestAllIsSorted(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("registry is empty")
	}
	for i := 1; i < len(all); i++ {
		prev, cur := all[i-1], all[i]
		pr, cr := familyRank(prev.Family), familyRank(cur.Family)
		if pr > cr || (pr == cr && prev.ID >= cur.ID) {
			t.Errorf("out of order at %d: %s then %s", i, prev.ID, cur.ID)
		}
	}
}

func TestCategoriesFollowDeclarationOrder(t *testing.T) {
	want := []string{
		catBase, catDataplane, catSecurity, catStorage, catTime,
		catNetwork, catRegistryPKI, catResidue, catCertificate,
	}
	got := Categories(FamilyPreflight)
	if len(got) != len(want) {
		t.Fatalf("got %d categories %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("category %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- helpers ---------------------------------------------------------------

func assertSameSet(t *testing.T, label string, want, got []string) {
	t.Helper()
	w := append([]string(nil), want...)
	g := append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)

	inW := map[string]bool{}
	for _, s := range w {
		inW[s] = true
	}
	inG := map[string]bool{}
	for _, s := range g {
		inG[s] = true
	}
	for _, s := range w {
		if !inG[s] {
			t.Errorf("%s: %s is expected but not registered", label, s)
		}
	}
	for _, s := range g {
		if !inW[s] {
			t.Errorf("%s: %s is registered but not in the expected inventory", label, s)
		}
	}
	if len(w) != len(g) {
		t.Errorf("%s: registered %d codes, expected %d", label, len(g), len(w))
	}
	if strings.Join(w, ",") != strings.Join(g, ",") {
		return // individual diffs already reported above
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
