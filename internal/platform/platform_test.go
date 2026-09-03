package platform

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/rke2"
)

func gitSpec(apps ...string) v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Platform: v1alpha1.PlatformSpec{GitOps: v1alpha1.GitOpsSpec{
			Source:        v1alpha1.GitOpsGit,
			GitRepo:       "https://github.com/argoproj/argocd-example-apps.git",
			Branch:        "master",
			BootstrapApps: apps,
		}},
	}
}

func ociSpec(repo string, apps ...string) v1alpha1.ClusterSpec {
	return v1alpha1.ClusterSpec{
		Platform: v1alpha1.PlatformSpec{GitOps: v1alpha1.GitOpsSpec{
			Source:        v1alpha1.GitOpsOCI,
			OCIRepo:       repo,
			BootstrapApps: apps,
		}},
	}
}

func docs(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(body))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			break
		}
		if len(d) > 0 {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		t.Fatalf("nothing parsed from:\n%s", body)
	}
	return out
}

func stepNames(steps []engine.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		if st, ok := s.(*engine.ShellStep); ok {
			out = append(out, st.Name)
			continue
		}
		out = append(out, s.ID())
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The profile baselines set `platform.gitops.source` for every profile, so
// treating that as the ask would put a GitOps controller on every cluster this
// tool builds -- including the ones whose operator never wanted one.
func TestSourceAloneDoesNotInstallArgoCD(t *testing.T) {
	spec := v1alpha1.ClusterSpec{
		Platform: v1alpha1.PlatformSpec{GitOps: v1alpha1.GitOpsSpec{Source: v1alpha1.GitOpsGit}},
	}
	if steps := Steps(&exec.Fake{}, spec, Material{}, Options{}); len(steps) > 0 {
		t.Errorf("a defaulted source installed ArgoCD: %v", stepNames(steps))
	}
}

func TestWantedIsDecidedByTheDocument(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name string
		spec v1alpha1.GitOpsSpec
		want bool
	}{
		{"a repository is the ask", gitSpec().Platform.GitOps, true},
		{"enabled false overrides a repository",
			v1alpha1.GitOpsSpec{Enabled: &no, GitRepo: "https://git.example/x.git"}, false},
		{"enabled true with no repository still installs",
			v1alpha1.GitOpsSpec{Enabled: &yes}, true},
		{"nothing at all", v1alpha1.GitOpsSpec{}, false},
		// The repository that counts is the one the source type points at:
		// a git URL left over in a document switched to OCI is not an ask.
		{"the other source's repository does not count",
			v1alpha1.GitOpsSpec{Source: v1alpha1.GitOpsOCI, GitRepo: "https://git.example/x.git"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wanted(tc.spec); got != tc.want {
				t.Errorf("wanted = %v, want %v", got, tc.want)
			}
		})
	}
}

// The chart names its objects <release>-<chart>-<component>, which would give
// "argocd-argo-cd-server" -- a name in no ArgoCD runbook. The release name
// alone is not enough to fix it here, unlike cert-manager.
func TestObjectNamesAreTheUpstreamOnes(t *testing.T) {
	body := Chart(gitSpec(), Options{})
	d := docs(t, body)[0]

	meta, _ := d["metadata"].(map[string]any)
	if meta["name"] != ReleaseName {
		t.Errorf("the release is named %v, want %q", meta["name"], ReleaseName)
	}

	values, _ := d["spec"].(map[string]any)["valuesContent"].(string)
	var v map[string]any
	if err := yaml.Unmarshal([]byte(values), &v); err != nil {
		t.Fatalf("the values are not YAML: %v", err)
	}
	if v["fullnameOverride"] != "argocd" {
		t.Errorf("fullnameOverride is %v; without it every object is named argocd-argo-cd-*", v["fullnameOverride"])
	}
}

// An airgap bundle carries these exact images. A floating version means the
// bundle and the manifest disagree about what is installed.
func TestChartVersionIsPinned(t *testing.T) {
	spec, _ := docs(t, Chart(gitSpec(), Options{}))[0]["spec"].(map[string]any)
	if spec["version"] != ChartVersion {
		t.Errorf("the chart version is %v, want %q", spec["version"], ChartVersion)
	}
	for _, bad := range []string{"", "*", "latest"} {
		if ChartVersion == bad {
			t.Errorf("the pinned version is %q, which is not a pin", bad)
		}
	}
}

func TestPrivateRegistryReachesTheChart(t *testing.T) {
	spec := gitSpec()
	spec.Registry.Mode = v1alpha1.RegistryExternal
	spec.Registry.SystemDefaultRegistry = "harbor.acme.internal"

	values, _ := docs(t, Chart(spec, Options{}))[0]["spec"].(map[string]any)["valuesContent"].(string)
	for _, want := range []string{
		"harbor.acme.internal/argoproj/argocd",
		"harbor.acme.internal/docker/library/redis",
	} {
		if !strings.Contains(values, want) {
			t.Errorf("the values do not point %q at the private registry:\n%s", want, values)
		}
	}

	// The embedded registry is RKE2's own mirror and is not a chart source, so
	// rewriting image repositories at it would point every pull at nothing.
	spec.Registry.Mode = v1alpha1.RegistryEmbedded
	values, _ = docs(t, Chart(spec, Options{}))[0]["spec"].(map[string]any)["valuesContent"].(string)
	if strings.Contains(values, "harbor.acme.internal") {
		t.Errorf("an embedded registry was written into the image repositories:\n%s", values)
	}
}

// An unpinned chart means the bundle that crossed the air gap and the cluster
// disagree about what is installed.
func TestOCIBootstrapAppNeedsAVersion(t *testing.T) {
	_, err := Applications(ociSpec("harbor.acme.internal/charts", "podinfo"))
	if err == nil {
		t.Fatal("an OCI bootstrap app was accepted without a chart version")
	}
	if !strings.Contains(err.Error(), "podinfo@<version>") {
		t.Errorf("the refusal does not say how to write it: %v", err)
	}

	apps, err := Applications(ociSpec("harbor.acme.internal/charts", "podinfo@6.9.2"))
	if err != nil {
		t.Fatalf("a pinned chart was refused: %v", err)
	}
	src, _ := docs(t, apps[0].Manifest)[0]["spec"].(map[string]any)["source"].(map[string]any)
	if src["chart"] != "podinfo" || src["targetRevision"] != "6.9.2" {
		t.Errorf("the source is %v", src)
	}
}

// A git bootstrap app is a path in the repository, and the branch the document
// names is what it tracks.
func TestGitBootstrapApp(t *testing.T) {
	apps, err := Applications(gitSpec("clusters/homelab/guestbook"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("%d applications", len(apps))
	}
	// The last segment, because "clusters-homelab-guestbook" is a name nobody
	// types twice.
	if apps[0].Name != "guestbook" {
		t.Errorf("the application is named %q", apps[0].Name)
	}

	d := docs(t, apps[0].Manifest)[0]
	spec, _ := d["spec"].(map[string]any)
	src, _ := spec["source"].(map[string]any)
	if src["path"] != "clusters/homelab/guestbook" || src["targetRevision"] != "master" {
		t.Errorf("the source is %v", src)
	}
	dst, _ := spec["destination"].(map[string]any)
	if dst["namespace"] != "guestbook" {
		t.Errorf("the destination is %v", dst)
	}
	if opts, _ := spec["syncPolicy"].(map[string]any); opts == nil {
		t.Error("the application has no sync policy, so nothing would ever deploy")
	}
}

// Removing a bootstrap entry says "stop managing this", which is not the same
// sentence as "delete the workload".
func TestApplicationsHaveNoCascadingFinalizer(t *testing.T) {
	apps, err := Applications(gitSpec("guestbook"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(apps[0].Manifest, "resources-finalizer") {
		t.Errorf("deleting this Application would delete everything it deployed:\n%s", apps[0].Manifest)
	}
}

// An Application with nowhere to sync from cannot exist, and a phase that
// silently produced no steps would report success.
func TestBootstrapAppsWithoutARepositoryFail(t *testing.T) {
	yes := true
	spec := v1alpha1.ClusterSpec{Platform: v1alpha1.PlatformSpec{GitOps: v1alpha1.GitOpsSpec{
		Enabled: &yes, Source: v1alpha1.GitOpsGit, BootstrapApps: []string{"guestbook"},
	}}}

	steps := Steps(&exec.Fake{}, spec, Material{}, Options{})
	if len(steps) != 1 {
		t.Fatalf("steps = %v", stepNames(steps))
	}
	if err := steps[0].Apply(context.Background()); err == nil {
		t.Fatal("the step succeeded")
	} else if !strings.Contains(err.Error(), "gitRepo") {
		t.Errorf("the failure does not say what to set: %v", err)
	}
}

// The repository has to be registered before the Applications that reference
// it, or the failure names an authentication problem rather than a missing
// credential.
func TestRepositoryComesBeforeTheApplications(t *testing.T) {
	got := stepNames(Steps(&exec.Fake{}, gitSpec("guestbook"), Material{}, Options{}))
	repo, apps := indexOf(got, "repository"), indexOf(got, "bootstrap-apps")
	if repo < 0 || apps < 0 {
		t.Fatalf("steps = %v", got)
	}
	if repo > apps {
		t.Errorf("the repository is registered after the applications: %v", got)
	}
	// And the sync check is last, because it is the one that proves the rest
	// worked.
	if got[len(got)-1] != "bootstrap-synced" {
		t.Errorf("the last step is %q", got[len(got)-1])
	}
}

// Sending the registry's password to github.com because both are configured in
// the same document is how a credential leaves the place it was meant for.
func TestRegistryCredentialOnlyGoesToTheRegistry(t *testing.T) {
	m := Material{
		RegistryHost: "harbor.acme.internal",
		RegistryUser: "robot$malmok",
		RegistryPass: "s3cret",
	}
	tests := []struct {
		name, repo string
		want       bool
	}{
		{"the same registry, written bare", "harbor.acme.internal/charts", true},
		{"the same registry, written with a scheme", "https://harbor.acme.internal/charts", true},
		{"somebody else entirely", "https://github.com/acme/apps.git", false},
		// A host that merely ends in the registry's name is a different host.
		{"a lookalike host", "https://evil-harbor.acme.internal/charts", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user, pass := m.credentialsFor(tc.repo)
			if got := user != "" || pass != ""; got != tc.want {
				t.Errorf("credentialsFor(%q) = %q/%q", tc.repo, user, pass)
			}
		})
	}
}

// A password in a file under the manifest directory would sit on the node's
// disk for the life of the cluster.
func TestTheRepositoryPasswordIsNeverWrittenToTheManifestDirectory(t *testing.T) {
	m := Material{
		RegistryHost: "harbor.acme.internal",
		RegistryUser: "robot$malmok",
		RegistryPass: "s3cret",
	}
	for _, s := range Steps(&exec.Fake{}, ociSpec("harbor.acme.internal/charts", "podinfo@6.9.2"), m, Options{}) {
		st, ok := s.(*engine.ShellStep)
		if !ok {
			continue
		}
		for _, cmd := range []string{st.Check, st.Do} {
			if !strings.Contains(cmd, m.RegistryPass) {
				continue
			}
			if strings.Contains(cmd, "mktemp") {
				continue // applied from a temporary file under umask
			}
			if strings.Contains(cmd, rke2.ManifestDir) {
				t.Errorf("%s writes the password into the manifest directory", st.Name)
			}
		}
	}
}

// The event stream is an audit artifact that gets handed to customers.
func TestTheCredentialNeverReachesTheEventStream(t *testing.T) {
	m := Material{RegistryHost: "harbor.acme.internal", RegistryUser: "robot", RegistryPass: "s3cret"}
	for _, s := range Steps(&exec.Fake{}, ociSpec("harbor.acme.internal/charts"), m, Options{}) {
		st, ok := s.(*engine.ShellStep)
		if !ok {
			continue
		}
		for _, sentence := range []string{st.Satisfied, st.Missing} {
			if strings.Contains(sentence, m.RegistryPass) {
				t.Errorf("%s puts the password in the event stream: %q", st.Name, sentence)
			}
		}
	}

	// The check compares a fingerprint, so a rotated credential is noticed
	// without the password being read back off the cluster.
	if fingerprint("robot", "s3cret") == fingerprint("robot", "other") {
		t.Error("two different credentials have the same fingerprint")
	}
}

// A chart deployed is not a controller that works. An Application only reaches
// a status once the API accepted it, the repository was reachable and the
// manifests rendered -- and nothing before this step establishes any of that.
func TestTheLastWordIsAnApplicationThatActuallyDeployed(t *testing.T) {
	steps := Steps(&exec.Fake{}, gitSpec("guestbook"), Material{}, Options{})
	last, ok := steps[len(steps)-1].(*engine.ShellStep)
	if !ok {
		t.Fatalf("the last step is %T", steps[len(steps)-1])
	}
	for _, want := range []string{"Synced/Healthy", "guestbook"} {
		if !strings.Contains(last.Check, want) {
			t.Errorf("the check does not look for %q:\n%s", want, last.Check)
		}
	}
	// A step whose Apply is itself a bounded wait must not be retried: three
	// attempts at a ten-minute wait take thirty minutes to say what is wrong.
	if last.Attempts != 1 {
		t.Errorf("the wait is retried %d times", last.Attempts)
	}
}

// The readiness check dry-runs the object that is about to be created, not a
// stand-in that might be accepted when the real one is not.
func TestReadinessDryRunsTheRealApplication(t *testing.T) {
	steps := Steps(&exec.Fake{}, gitSpec("guestbook"), Material{}, Options{})
	ready, ok := steps[1].(*engine.ShellStep)
	if !ok || ready.Name != "argocd-ready" {
		t.Fatalf("the second step is %v", stepNames(steps))
	}
	if !strings.Contains(ready.Check, "--dry-run=server") {
		t.Errorf("the readiness check is not a dry run:\n%s", ready.Check)
	}
	if !strings.Contains(ready.Check, "guestbook") {
		t.Errorf("the readiness check does not use the real application:\n%s", ready.Check)
	}
}

func TestObserveChangesNothing(t *testing.T) {
	f := &exec.Fake{Default: exec.Result{ExitCode: 1}}
	m := Material{RegistryHost: "harbor.acme.internal", RegistryUser: "robot", RegistryPass: "s3cret"}

	for _, s := range Steps(f, ociSpec("harbor.acme.internal/charts", "podinfo@6.9.2"), m, Options{}) {
		if _, err := s.Observe(context.Background()); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	for _, cmd := range f.Log {
		for _, bad := range []string{
			"kubectl apply", "kubectl create", "kubectl delete", "install -d", "mktemp", "> /var/lib",
		} {
			if !strings.Contains(cmd, bad) {
				continue
			}
			// A server-side dry run is the exception and the only one: the API
			// server runs admission and discards the result, which is what
			// makes it a measurement rather than a change.
			if bad == "kubectl apply" && strings.Contains(cmd, "--dry-run=server") {
				continue
			}
			t.Errorf("an Observe would have changed the cluster: it contains %q\n%s", bad, cmd)
		}
	}

	// `rollout status` reads; the timeout keeps a check from becoming a wait.
	for _, cmd := range f.Log {
		if strings.Contains(cmd, "rollout status") && !strings.Contains(cmd, "--timeout=5s") {
			t.Errorf("a check waits on a rollout without a short timeout:\n%s", cmd)
		}
	}
}

func TestStepIDs(t *testing.T) {
	for _, s := range Steps(&exec.Fake{}, gitSpec("guestbook"), Material{}, Options{}) {
		name, host, ok := strings.Cut(s.ID(), "@")
		if !ok || host != "fake" {
			t.Errorf("%q does not end in the node", s.ID())
		}
		if strings.Contains(name, "@") {
			t.Errorf("the step part of %q contains an '@'", s.ID())
		}
		if !strings.HasPrefix(name, Phase+"/") {
			t.Errorf("%q is not filed under %s", s.ID(), Phase)
		}
	}
}

// Every manifest this phase writes carries the marker PF-802 reads, or the next
// preflight blocks on a cluster this tool built.
func TestManifestsAreMarkedAsOurs(t *testing.T) {
	apps, err := Applications(gitSpec("guestbook"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	for name, body := range map[string]string{
		"chart":       Chart(gitSpec(), Options{}),
		"application": apps[0].Manifest,
	} {
		if !strings.HasPrefix(body, managedFileHeader) {
			t.Errorf("the %s manifest is not marked as managed:\n%s", name, body)
		}
	}
}

func indexOf(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}
