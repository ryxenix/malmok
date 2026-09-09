package nodeprep

import (
	"context"
	"strings"
	"testing"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
)

func embeddedSpec() v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Network:  v1alpha1.NetworkSpec{Mode: v1alpha1.NetworkOnline},
		Registry: v1alpha1.RegistrySpec{Mode: v1alpha1.RegistryEmbedded},
	}
}

// Observe must not change anything. This is what resume depends on: a process
// killed mid-step leaves a record saying "running", and the only way back is to
// look at the world again (§4.2 rule 4).
func TestObserveChangesNothing(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1}}
	for _, s := range Steps(f, "10.0.0.11", embeddedSpec(), TrustMaterial{}) {
		if _, err := s.Observe(context.Background()); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}

	mutating := []string{
		"modprobe ", "sysctl --system", "sysctl -w", "swapoff", "install -d",
		"update-ca-", "> /etc/", "sed -i", "chmod ", "systemctl start",
	}
	for _, cmd := range f.Log {
		for _, bad := range mutating {
			if strings.Contains(cmd, bad) {
				t.Errorf("an Observe would have changed the node: it contains %q\n%s", bad, cmd)
			}
		}
	}
}

// The runner splits a step key on the first '@', so a step id may not contain
// one -- the constraint is on the ids this package authors (§4.1).
func TestStepIDsCarryOneAt(t *testing.T) {
	for _, s := range Steps(&exec.Fake{}, "node@example.com", embeddedSpec(), TrustMaterial{}) {
		id := s.ID()
		name, host, ok := strings.Cut(id, "@")
		if !ok {
			t.Fatalf("%q has no node", id)
		}
		if strings.Contains(name, "@") {
			t.Errorf("the step part of %q contains an '@'", id)
		}
		if host != "node@example.com" {
			t.Errorf("the node part of %q is %q", id, host)
		}
		if !strings.HasPrefix(name, Phase+"/") {
			t.Errorf("%q is not filed under %s", id, Phase)
		}
	}
}

// A non-zero Check is an answer, not a fault: it means the target state does
// not hold yet. Only a transport failure is an error.
func TestObserveDistinguishesUnsatisfiedFromUnreachable(t *testing.T) {
	t.Run("unsatisfied", func(t *testing.T) {
		f := &exec.Fake{Default: exec.Result{ExitCode: 1, Stdout: "net.ipv4.ip_forward is 0, want 1\n"}}
		s := &engine.ShellStep{Name: "sysctl", Host: "10.0.0.11", Runner: f, Check: "check", Missing: "%s"}

		obs, err := s.Observe(context.Background())
		if err != nil {
			t.Fatalf("an unsatisfied check was reported as an error: %v", err)
		}
		if obs.Satisfied {
			t.Error("a failing check reported the state as satisfied")
		}
		if !strings.Contains(obs.Detail, "ip_forward is 0") {
			t.Errorf("the node's own words were dropped: %q", obs.Detail)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		f := &exec.Fake{Err: exec.ErrNotConnected}
		s := &engine.ShellStep{Name: "sysctl", Host: "10.0.0.11", Runner: f, Check: "check"}

		if _, err := s.Observe(context.Background()); err == nil {
			t.Fatal("a node that could not be reached reported an observation")
		}
	})
}

// A failing Apply has to carry the node's own words. A restatement of the step
// name tells whoever is holding the pager nothing.
func TestApplyCarriesTheNodesWords(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1, Stderr: "sysctl: permission denied\n"}}
	s := &engine.ShellStep{Name: "sysctl", Host: "10.0.0.11", Runner: f, Do: "do"}

	err := s.Apply(context.Background())
	if err == nil {
		t.Fatal("a failing command reported success")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the node's message was dropped: %v", err)
	}
	if !strings.Contains(err.Error(), "EX-002") {
		t.Errorf("the failure carries no diagnostic code: %v", err)
	}
}

// Evidence goes into the audit report, and event.Validate refuses control
// sequences and newlines in it (§5.3).
func TestEvidenceIsCleanedForTheEventSchema(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{Stdout: "line one\n\x1b[31mred\x1b[0m\tline two\r\n"}}
	s := &engine.ShellStep{Name: "x", Host: "h", Runner: f, Check: "check", Satisfied: "%s"}

	obs, err := s.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{obs.Detail, obs.Evidence} {
		if strings.ContainsAny(field, "\n\r\t") {
			t.Errorf("%q still holds whitespace the schema refuses", field)
		}
		if strings.ContainsRune(field, 0x1b) {
			t.Errorf("%q still holds an ANSI escape", field)
		}
	}
}

// Both halves of the swap step matter, and for different reasons: the kubelet
// refuses to start with swap active, and an fstab entry left behind brings it
// back at the next reboot.
func TestSwapChecksFstabAndNotOnlyTheRunningState(t *testing.T) {
	check := swapStep().Check
	if !strings.Contains(check, "/etc/fstab") {
		t.Error("the swap check looks at the running state only; a reboot would undo it")
	}

	do := swapStep().Do
	if !strings.Contains(do, "fstab.malmok.bak") {
		t.Error("fstab is edited without a backup")
	}
	// The line is commented rather than deleted, so an operator can see what
	// was there and put it back.
	if strings.Contains(do, "sed -i") && !strings.Contains(do, "# malmok disabled swap") {
		t.Error("the fstab entry is removed rather than commented out")
	}
}

// Every file this phase writes has to say what put it there. Without it an
// operator finds a sysctl they did not write and cannot tell whether removing
// it breaks something.
func TestWrittenFilesAreMarkedAsManaged(t *testing.T) {
	for _, s := range Steps(&exec.Fake{}, "10.0.0.11", embeddedSpec(), TrustMaterial{}) {
		st := s.(*engine.ShellStep)
		if !strings.Contains(st.Do, ">") {
			continue // writes no file
		}
		if st.Name == "swap" || st.Name == "datadir" {
			continue // edits an existing file, or creates a directory
		}
		if !strings.Contains(st.Do, managedFileHeader) {
			t.Errorf("%s writes a file with no marker saying what put it there", st.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// registries.yaml
// ---------------------------------------------------------------------------

func TestRegistriesYAML(t *testing.T) {
	// This case used to assert the opposite, and the opposite was wrong. RKE2's
	// embedded mirror takes part in a registry only when that registry is named
	// under mirrors: in the node's registries.yaml -- the entry carries no
	// endpoint, because the peers are the endpoint. Writing no file left the
	// mirror enabled cluster-wide and mirroring nothing.
	t.Run("the embedded mirror needs a mirrors entry on every node", func(t *testing.T) {
		got := registriesYAML(embeddedSpec(), TrustMaterial{})
		if got == "" {
			t.Fatal("no registries.yaml was rendered, so no registry takes part in the mirror")
		}
		if !strings.Contains(got, "mirrors:") || !strings.Contains(got, `"*":`) {
			t.Errorf("the file names no registry to mirror:\n%s", got)
		}
		// An endpoint would send containerd to that address instead of to the
		// peers, which is the opposite of what this mode is for.
		if strings.Contains(got, "endpoint:") {
			t.Errorf("the embedded mirror was given an endpoint:\n%s", got)
		}
		var found bool
		for _, s := range Steps(&exec.Fake{}, "h", embeddedSpec(), TrustMaterial{}) {
			if s.(*engine.ShellStep).Name == "registries" {
				found = true
			}
		}
		if !found {
			t.Error("the registries step is absent, so the file never reaches the node")
		}
	})

	t.Run("a system default registry mirrors everything", func(t *testing.T) {
		spec := embeddedSpec()
		spec.Registry = v1alpha1.RegistrySpec{
			Mode: v1alpha1.RegistryExternal, SystemDefaultRegistry: "harbor.acme.internal",
		}
		got := registriesYAML(spec, TrustMaterial{
			RegistryUser: "robot", RegistryPass: "s3cret", CABundle: []byte("pem"),
		})
		for _, want := range []string{
			`"*":`, "https://harbor.acme.internal", "username: \"robot\"",
			"password: \"s3cret\"", "ca_file:",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the rendered file is missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("a password with yaml punctuation stays one value", func(t *testing.T) {
		spec := embeddedSpec()
		spec.Registry = v1alpha1.RegistrySpec{
			Mode: v1alpha1.RegistryExternal, SystemDefaultRegistry: "harbor.acme.internal",
		}
		got := registriesYAML(spec, TrustMaterial{RegistryUser: "robot", RegistryPass: `a: b #c"d`})
		if !strings.Contains(got, `password: "a: b #c\"d"`) {
			t.Errorf("the password was not quoted:\n%s", got)
		}
	})

	// registry.mirrors is in the schema, is documented, and the network
	// preflight dials the endpoints and reports them reachable. None of that
	// put them on a node: the renderer returned before it read the field
	// whenever the mode was embedded, empty, or named no private registry.
	t.Run("a named mirror reaches the node whatever the mode is", func(t *testing.T) {
		for _, mode := range []v1alpha1.RegistryMode{
			v1alpha1.RegistryEmbedded, v1alpha1.RegistryUpstream, "",
		} {
			spec := embeddedSpec()
			spec.Registry = v1alpha1.RegistrySpec{
				Mode:    mode,
				Mirrors: map[string][]string{"docker.io": {"http://192.168.88.253:5100"}},
			}
			got := registriesYAML(spec, TrustMaterial{})
			if !strings.Contains(got, `"docker.io":`) ||
				!strings.Contains(got, "http://192.168.88.253:5100") {
				t.Errorf("mode %q dropped the mirror the document named:\n%s", mode, got)
			}
			// http has no certificate to verify, so an entry describing one
			// would be a TLS setting for a connection that carries no TLS.
			if strings.Contains(got, "configs:") {
				t.Errorf("mode %q described TLS for an http endpoint:\n%s", mode, got)
			}
		}
	})

	// The embedded mirror and a cache are not alternatives: RKE2 tries the
	// peers first and the listed endpoint after them, so a cluster shares what
	// it already holds and reaches the cache for what it does not.
	t.Run("the embedded mirror keeps every registry while a mirror is named", func(t *testing.T) {
		spec := embeddedSpec()
		spec.Registry = v1alpha1.RegistrySpec{
			Mode:    v1alpha1.RegistryEmbedded,
			Mirrors: map[string][]string{"docker.io": {"http://cache:5100"}},
		}
		got := registriesYAML(spec, TrustMaterial{})
		if !strings.Contains(got, `"*":`) {
			t.Errorf("only docker.io takes part now, so nothing else is shared peer to peer:\n%s", got)
		}
		if strings.Count(got, `"*":`) != 1 {
			t.Errorf("the wildcard is written twice, which is a duplicate key:\n%s", got)
		}
	})

	// The step writes this file and then compares what it finds against what
	// it meant to write. Map order made that comparison report drift the step
	// had caused itself, on any document with more than one mirror.
	t.Run("two mirrors render the same file every time", func(t *testing.T) {
		spec := embeddedSpec()
		spec.Registry = v1alpha1.RegistrySpec{
			Mode: v1alpha1.RegistryEmbedded,
			Mirrors: map[string][]string{
				"docker.io":       {"http://cache:5100"},
				"registry.k8s.io": {"http://cache:5101"},
				"quay.io":         {"http://cache:5102"},
				"ghcr.io":         {"http://cache:5103"},
			},
		}
		first := registriesYAML(spec, TrustMaterial{})
		for i := 0; i < 20; i++ {
			if got := registriesYAML(spec, TrustMaterial{}); got != first {
				t.Fatalf("run %d rendered a different file:\n%s\n---\n%s", i, first, got)
			}
		}
	})

	// TrustSpec's own comment calls a missing registries.yaml CA the single
	// most common private-CA misinstall. A mirror endpoint on https was
	// exactly that: described nowhere, so every pull through it failed with an
	// opaque x509 error.
	t.Run("an https mirror is given the CA the private registry gets", func(t *testing.T) {
		spec := embeddedSpec()
		spec.Registry = v1alpha1.RegistrySpec{
			Mode:    v1alpha1.RegistryEmbedded,
			Mirrors: map[string][]string{"docker.io": {"cache.acme.internal"}},
		}
		got := registriesYAML(spec, TrustMaterial{CABundle: []byte("pem")})
		if !strings.Contains(got, `"cache.acme.internal":`) || !strings.Contains(got, "ca_file:") {
			t.Errorf("the https mirror got no CA:\n%s", got)
		}
	})

	// A cache is somebody else's endpoint. The registry's credentials are for
	// the registry.
	t.Run("a mirror is not sent the registry credentials", func(t *testing.T) {
		spec := embeddedSpec()
		spec.Registry = v1alpha1.RegistrySpec{
			Mode:                  v1alpha1.RegistryExternal,
			SystemDefaultRegistry: "harbor.acme.internal",
			Mirrors:               map[string][]string{"docker.io": {"cache.acme.internal"}},
		}
		got := registriesYAML(spec, TrustMaterial{RegistryUser: "robot", RegistryPass: "s3cret"})
		cache := got[strings.Index(got, `"cache.acme.internal":`):]
		if i := strings.Index(cache[1:], `"harbor`); i >= 0 {
			cache = cache[:i+1]
		}
		if strings.Contains(cache, "s3cret") {
			t.Errorf("the registry password was handed to the cache:\n%s", got)
		}
	})

	t.Run("insecure skips verification instead of naming a CA", func(t *testing.T) {
		spec := embeddedSpec()
		yes := true
		spec.Registry = v1alpha1.RegistrySpec{
			Mode: v1alpha1.RegistryExternal, SystemDefaultRegistry: "h", Insecure: &yes,
		}
		got := registriesYAML(spec, TrustMaterial{CABundle: []byte("pem")})
		if !strings.Contains(got, "insecure_skip_verify: true") {
			t.Errorf("insecure was ignored:\n%s", got)
		}
	})
}

// The registries file holds a registry password. Its evidence reaches the audit
// report, and the report reaches the customer.
func TestRegistriesStepNeverPrintsItsContents(t *testing.T) {
	spec := embeddedSpec()
	spec.Registry = v1alpha1.RegistrySpec{
		Mode: v1alpha1.RegistryExternal, SystemDefaultRegistry: "harbor.acme.internal",
	}
	steps := Steps(&exec.Fake{}, "h", spec, TrustMaterial{RegistryUser: "robot", RegistryPass: "s3cret"})

	var found *engine.ShellStep
	for _, s := range steps {
		if s.(*engine.ShellStep).Name == "registries" {
			found = s.(*engine.ShellStep)
		}
	}
	if found == nil {
		t.Fatal("no registries step was produced for an external registry")
	}
	if !strings.Contains(found.Do, "chmod 0600") {
		t.Error("the file is written without restricting its mode")
	}

	f := &exec.Fake{Default: exec.Result{ExitCode: 1, Stdout: "/etc/rancher/rke2/registries.yaml differs from the document\n"}}
	found.Runner = f
	obs, err := found.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(obs.Detail+obs.Evidence, "s3cret") {
		t.Errorf("the password reached the event stream: %q %q", obs.Detail, obs.Evidence)
	}
}

// ---------------------------------------------------------------------------
// CA trust
// ---------------------------------------------------------------------------

// docs/11-execute.md §2.1: the trust store belongs to l0 because L1 pulls
// images before any of the cluster exists.
func TestTrustStepOnlyExistsWithMaterial(t *testing.T) {
	if hasStep(Steps(&exec.Fake{}, "h", embeddedSpec(), TrustMaterial{}), "ca-trust") {
		t.Error("a trust step was produced with no CA to install")
	}
	with := Steps(&exec.Fake{}, "h", embeddedSpec(), TrustMaterial{CABundle: []byte("-----BEGIN CERTIFICATE-----\n")})
	if !hasStep(with, "ca-trust") {
		t.Error("no trust step was produced for a private CA")
	}
}

// Both families rebuild a bundle from a directory, but from different
// directories with different commands. That is the one place the family
// actually matters, and getting it wrong installs the CA nowhere.
func TestTrustStepHandlesBothFamilies(t *testing.T) {
	s := trustStep(TrustMaterial{CABundle: []byte("pem")})
	for _, want := range []string{caFileDebian, caFileRHEL, "update-ca-trust", "update-ca-certificates"} {
		if !strings.Contains(s.Check+s.Do, want) {
			t.Errorf("the trust step never mentions %q", want)
		}
	}
}

// A CA that differs from the document has to be replaced, not accepted: a node
// carrying last year's CA fails every pull with an opaque x509 error.
func TestTrustStepComparesContentNotPresence(t *testing.T) {
	s := trustStep(TrustMaterial{CABundle: []byte("pem")})
	if !strings.Contains(s.Check, "cmp -s") {
		t.Error("the trust check only looks for the file, not at what is in it")
	}
}

func hasStep(steps []engine.Step, name string) bool {
	for _, s := range steps {
		if s.(*engine.ShellStep).Name == name {
			return true
		}
	}
	return false
}

// A document that keeps its swap gets no swap step. The field used to be read
// by nothing, so `disableSwap: false` was a statement the tool contradicted
// the moment it ran -- swap off, fstab rewritten, nothing said.
func TestSwapIsLeftAloneWhenTheDocumentSaysSo(t *testing.T) {
	names := func(s v1alpha1.ClusterSpec) []string {
		var out []string
		for _, step := range Steps(&exec.Fake{}, "192.0.2.10", s, TrustMaterial{}) {
			out = append(out, step.(*engine.ShellStep).Name)
		}
		return out
	}

	has := func(list []string, want string) bool {
		for _, n := range list {
			if n == want {
				return true
			}
		}
		return false
	}

	unset := v1alpha1.ClusterSpec{}
	if !has(names(unset), "swap") {
		t.Error("an unset disableSwap does not turn swap off; that is the only default that boots")
	}

	on := v1alpha1.ClusterSpec{}
	yes := true
	on.OS.DisableSwap = &yes
	if !has(names(on), "swap") {
		t.Error("disableSwap: true does not turn swap off")
	}

	keep := v1alpha1.ClusterSpec{}
	no := false
	keep.OS.DisableSwap = &no
	if has(names(keep), "swap") {
		t.Error("disableSwap: false still turns swap off, which is what the field exists to prevent")
	}
}

// Every file this package writes goes through printf in a shell program, and
// Go's %q is the wrong quoting for that: the shell strips its quotes and hands
// printf a literal backslash-n, which printf '%s' writes as two characters.
//
// The result is a one-line file wherever the content had newlines. It reached
// a node twice -- registries.yaml, where RKE2 answered "no registries
// configured for distributed mirroring", and the CA trust store, where a PEM
// arrived as a single line of backslash-n and was not a certificate at all.
// Neither failed the step: both files existed and matched what the check
// compared them against, because the check was quoted the same wrong way.
// The newline guard passed while the script was broken, because raw material
// carries real newlines and that is all it looked for. What it missed is where
// those newlines land: a PEM dropped into a command unquoted puts its second
// line where the shell expects another command.
//
// That is what 0.88.0 shipped in ca-trust's Check. The certificate installed
// correctly and could never be confirmed, so every private-CA case halted on
// EX-003 -- applied, and the target state not reached -- two releases later, on
// hardware, naming the trust store rather than the quoting.
func TestMaterialIsQuotedWhereverItAppears(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	spec := v1alpha1.ClusterSpec{}
	spec.Registry.Mode = v1alpha1.RegistryEmbedded

	for _, s := range Steps(&exec.Fake{}, "192.0.2.10", spec, TrustMaterial{CABundle: []byte(pem)}) {
		sh := s.(*engine.ShellStep)
		for half, program := range map[string]string{"Check": sh.Check, "Do": sh.Do} {
			for i := 0; ; {
				at := strings.Index(program[i:], pem)
				if at < 0 {
					break
				}
				at += i
				// ShellQuote wraps in single quotes, so quoted material is
				// always preceded by one. Anything else is the shell reading
				// the second line as a command.
				if at == 0 || program[at-1] != '\'' {
					t.Errorf("%s %s inserts the certificate unquoted at byte %d:\n%s",
						sh.Name, half, at, program)
				}
				i = at + len(pem)
			}
		}
	}
}

func TestWrittenFilesKeepTheirNewlines(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	spec := v1alpha1.ClusterSpec{}
	spec.Registry.Mode = v1alpha1.RegistryEmbedded

	steps := Steps(&exec.Fake{}, "192.0.2.10", spec, TrustMaterial{CABundle: []byte(pem)})

	var checked int
	for _, s := range steps {
		sh := s.(*engine.ShellStep)
		if sh.Name != "ca-trust" && sh.Name != "registries" {
			continue
		}
		checked++
		for _, program := range []string{sh.Check, sh.Do} {
			// The two-character sequence, not a newline. Its presence means
			// the content was Go-quoted on its way into the shell.
			if strings.Contains(program, `\n`) {
				t.Errorf("%s writes an escaped newline rather than a real one:\n%s", sh.Name, program)
			}
		}
		if !strings.Contains(sh.Do, "\n") {
			t.Errorf("%s writes a single-line body", sh.Name)
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d steps, want the ca-trust and registries steps", checked)
	}
}
