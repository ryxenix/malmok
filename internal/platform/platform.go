// Package platform builds the l2-platform phase.
//
// The catalogue files four things under this phase -- GitOps, observability,
// secrets and the upgrade controller. Only GitOps is implemented, and that is a
// deliberate boundary rather than an unfinished list: ArgoCD is what the other
// three should be installed *by*. A platform tool that installs every add-on
// itself has to be re-run to change any of them, which is the coupling ADR-002
// and ADR-006 both exist to prevent. This tool builds the cluster up to the
// point where GitOps can take over, and hands over there.
//
// Nothing is applied ad hoc. Everything is written into RKE2's auto-deploying
// manifest directory, so the cluster reconciles it on restart without this tool
// present -- the difference between a cluster somebody can hand over and one
// that needs its installer kept around.
package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ryxen/malmok/api/v1alpha1"
	"github.com/ryxen/malmok/internal/engine"
	"github.com/ryxen/malmok/internal/exec"
	"github.com/ryxen/malmok/internal/rke2"
)

// Phase is where these steps are filed.
const Phase = "l2-platform"

// ChartVersion is pinned rather than tracked, for the same reason every other
// version in this tool is: an airgap bundle carries these exact images, and a
// floating version means the bundle and the manifest disagree about what is
// installed.
//
// Not the newest release. 10.3.x was published days ago and has already taken
// two patches; 10.2.3 carries the same ArgoCD (v3.5.0) with a release behind it
// that has stopped moving. A chart nobody has run yet is not what goes into a
// bundle that crosses an air gap and cannot be corrected in place.
const ChartVersion = "10.2.3"

// Namespace is where ArgoCD is installed.
const Namespace = "argocd"

// ReleaseName is the HelmChart resource name, which RKE2 uses as the Helm
// release name.
//
// It must not be changed after a cluster has been built with it. Helm stamps
// its release name onto every object it owns and ArgoCD's CRDs are
// cluster-scoped, so a rename leaves CRDs owned by a release that no longer
// exists and every later install fails with "cannot be imported into the
// current release" until somebody deletes them by hand. Deleting the namespace
// does not help; the CRDs are not in it.
const ReleaseName = "argocd"

// RepoSecretName holds the credential for the repository the document names.
const RepoSecretName = "malmok-repo"

// Files this phase writes.
const (
	argocdFile = rke2.ManifestDir + "/malmok-argocd.yaml"
	appsFile   = rke2.ManifestDir + "/malmok-argocd-apps.yaml"
)

const managedFileHeader = "# Managed by malmok. Changes here are overwritten on the next apply."

// fingerprintAnnotation is how the repository Secret says which credential it
// holds, so the check never has to read a password back off the cluster.
const fingerprintAnnotation = "platform.ryxen.dev/fingerprint"

// kubectl is the prelude every cluster-scoped step needs.
var kubectl = rke2.Kubectl

// Material is what the caller resolved from the document's SourceRefs.
//
// There is no separate GitOps credential in the schema, and that is right for
// the case ADR-007 describes: in an air gap the charts live in the same Harbor
// as the images, so the repository credential is the registry credential. The
// match is made here rather than by the caller because it is a decision about
// the Secret being rendered, not about reading a file.
type Material struct {
	// RegistryHost, RegistryUser and RegistryPass are the private registry's
	// credential, reused for a chart repository hosted on the same registry.
	RegistryHost string
	RegistryUser string
	RegistryPass string
}

// Options are the timeouts and the airgap chart source.
type Options struct {
	// ChartRepo overrides where the argo-cd chart comes from. An airgapped site
	// points this at a mirror that already holds it.
	ChartRepo string
	Timeout   time.Duration
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 10 * time.Minute
	}
	return o.Timeout
}

// UpstreamRepo is where this chart comes from when nothing mirrors it.
const UpstreamRepo = "https://argoproj.github.io/argo-helm"

// chartSource is where the chart comes from, in the shape a HelmChart wants.
// The document's mirror first, then the caller's override, then upstream.
func (o Options) chartSource(spec v1alpha1.ClusterSpec, chart string) string {
	repo := strings.TrimSpace(spec.Registry.ChartRepo)
	if repo == "" {
		repo = strings.TrimSpace(o.ChartRepo)
	}
	if repo == "" {
		repo = UpstreamRepo
	}
	return rke2.ChartSource(repo, chart)
}

// Steps returns the l2-platform catalogue.
//
// Empty when the document asks for no GitOps. Installing ArgoCD because a
// profile defaulted `platform.gitops.source` would leave a controller running
// on every cluster this tool builds, including the ones whose operator never
// asked for one.
func Steps(runner exec.Runner, spec v1alpha1.ClusterSpec, m Material, o Options) []engine.Step {
	g := spec.Platform.GitOps
	if !wanted(g) {
		return nil
	}

	host := runner.Host()
	add := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = Phase, runner, host
		return s
	}

	apps, err := Applications(spec)
	if err != nil {
		return []engine.Step{engine.FailedStep{
			StepID: Phase + "/bootstrap-apps@" + host,
			Why:    err.Error(),
		}}
	}

	// The probe object is a real bootstrap Application when there is one, so
	// the readiness check exercises exactly what is about to be created rather
	// than a stand-in that might be accepted when the real thing is not.
	probe := probeApplication()
	if len(apps) > 0 {
		probe = apps[0].Manifest
	}

	steps := []engine.Step{
		add(rke2.ManifestStep(Phase, "argocd", argocdFile, Chart(spec, o),
			"helmchart -n kube-system "+ReleaseName, o.timeout())),
		add(rke2.AcceptsStep(Phase, "argocd-ready", probe, "ArgoCD", o.timeout())),
		add(availableStep(o)),
	}

	// The repository comes before the Applications that reference it: an
	// Application pointing at an unregistered private repo reports a
	// authentication failure rather than a missing credential, and the two read
	// nothing alike.
	if repoURL(g) != "" {
		steps = append(steps, add(repoSecretStep(g, m)))
	}

	if len(apps) > 0 {
		steps = append(steps,
			add(rke2.ManifestStep(Phase, "bootstrap-apps", appsFile, appsManifest(apps),
				"application.argoproj.io -n "+Namespace+" "+strings.Join(names(apps), " "),
				o.timeout())),
			add(syncedStep(apps, o)),
		)
	}
	return steps
}

// wanted reports whether the document asks for ArgoCD.
//
// `enabled` decides when it is set. When it is not, a repository is taken as
// the ask: a document that names one has said what GitOps would reconcile from,
// and `source` alone has not, because the profile baselines set it for every
// profile whether or not anybody wants a controller.
func wanted(g v1alpha1.GitOpsSpec) bool {
	if g.Enabled != nil {
		return *g.Enabled
	}
	return repoURL(g) != ""
}

// repoURL is the repository the document's source type points at.
func repoURL(g v1alpha1.GitOpsSpec) string {
	if g.Source == v1alpha1.GitOpsOCI {
		return strings.TrimSpace(g.OCIRepo)
	}
	return strings.TrimSpace(g.GitRepo)
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// availableStep waits for the three workloads an Application needs.
//
// This is not proof that ArgoCD can sync -- only a real Application reaching a
// status proves that, which is what the bootstrap step below checks. It is here
// because without it the next step's failure would be "the Application has no
// status", which says nothing about which of the three is not running.
func availableStep(o Options) *engine.ShellStep {
	// The controller is a StatefulSet and the other two are Deployments, so
	// each is asked in its own vocabulary rather than through a single guess.
	workloads := []string{
		"statefulset/argocd-application-controller",
		"deployment/argocd-repo-server",
		"deployment/argocd-server",
	}

	var check, wait strings.Builder
	for _, w := range workloads {
		fmt.Fprintf(&check, `kubectl -n %s rollout status %s --timeout=5s >/dev/null 2>&1 || {
  echo "%s is not available yet"; exit 1; }
`, Namespace, w, w)
		fmt.Fprintf(&wait, `kubectl -n %s rollout status %s --timeout=%ds || exit 1
`, Namespace, w, int(o.timeout().Seconds()))
	}

	return &engine.ShellStep{
		Name:      "argocd-available",
		Check:     kubectl + check.String() + `echo "the ArgoCD controller, repo server and API server are available"`,
		Do:        kubectl + wait.String(),
		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: 3*o.timeout() + time.Minute,
		// Apply is already a bounded wait; retrying it just waits again.
		Attempts: 1,
	}
}

// repoSecretStep registers the repository ArgoCD reconciles from.
//
// Applied from a temporary file under umask rather than written into the
// manifest directory: that would keep a registry password on the node's disk
// for the life of the cluster. Applying it once puts the credential in etcd,
// where it was going anyway.
func repoSecretStep(g v1alpha1.GitOpsSpec, m Material) *engine.ShellStep {
	url := repoURL(g)
	user, pass := m.credentialsFor(url)
	fp := fingerprint(user, pass)

	var b strings.Builder
	b.WriteString(`apiVersion: v1
kind: Secret
metadata:
  name: ` + yamlString(RepoSecretName) + `
  namespace: ` + yamlString(Namespace) + `
  labels:
    argocd.argoproj.io/secret-type: repository
  annotations:
    ` + fingerprintAnnotation + `: ` + yamlString(fp) + `
type: Opaque
stringData:
  name: ` + yamlString("malmok") + `
  url: ` + yamlString(url) + `
`)
	if g.Source == v1alpha1.GitOpsOCI {
		// An OCI registry is a Helm repository to ArgoCD, and it has to be told
		// so: left as the default it is treated as a Git remote and the clone
		// fails with a transport error that names nothing useful.
		b.WriteString("  type: \"helm\"\n  enableOCI: \"true\"\n")
	} else {
		b.WriteString("  type: \"git\"\n")
	}
	if user != "" {
		b.WriteString("  username: " + yamlString(user) + "\n")
		b.WriteString("  password: " + yamlString(pass) + "\n")
	}

	return &engine.ShellStep{
		Name: "repository",
		Check: kubectl + fmt.Sprintf(`have=$(kubectl -n %s get secret %s -o jsonpath='{.metadata.annotations.%s}' 2>/dev/null)
[ -n "$have" ] || { echo "the repository %s is not registered with ArgoCD"; exit 1; }
[ "$have" = %s ] || { echo "%s is registered with a different credential"; exit 1; }
echo "%s is registered with ArgoCD"`,
			Namespace, RepoSecretName, jsonPathKey(fingerprintAnnotation),
			url, shellQuote(fp), url, url),

		Do: kubectl + fmt.Sprintf(`set -e
kubectl create namespace %s --dry-run=client -o yaml | kubectl apply -f - >/dev/null
umask 077
t=$(mktemp)
trap 'rm -f "$t"' EXIT
printf '%%s' %s > "$t"
kubectl apply -f "$t" >/dev/null`, Namespace, shellQuote(b.String())),

		Satisfied: "%s",
		Missing:   "%s",
	}
}

// syncedStep waits for the bootstrap Applications to actually deploy.
//
// This is the step that proves the phase did something. An Application object
// that exists proves the CRD is registered; an Application that reports Synced
// and Healthy proves the repository was reachable, the credential worked, the
// manifests rendered and the cluster accepted them -- the whole path GitOps
// needs, which nothing before it establishes.
func syncedStep(apps []Application, o Options) *engine.ShellStep {
	read := func(app string) string {
		return fmt.Sprintf(
			`kubectl -n %s get application %s -o jsonpath='{.status.sync.status}/{.status.health.status}' 2>/dev/null`,
			Namespace, app)
	}

	var check, wait strings.Builder
	for _, a := range apps {
		fmt.Fprintf(&check, `s=$(%s)
[ "$s" = "Synced/Healthy" ] || { echo "%s is ${s:-not reconciled yet}"; exit 1; }
`, read(a.Name), a.Name)

		fmt.Fprintf(&wait, `deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  [ "$(%s)" = "Synced/Healthy" ] && break
  sleep 5
done
if [ "$(%s)" != "Synced/Healthy" ]; then
  echo "%s never became Synced and Healthy. ArgoCD reports:"
  kubectl -n %s get application %s -o jsonpath='{.status.sync.status} {.status.health.status} {.status.conditions}' 2>&1
  exit 1
fi
`, int(o.timeout().Seconds()), read(a.Name), read(a.Name), a.Name, Namespace, a.Name)
	}

	return &engine.ShellStep{
		Name:      "bootstrap-synced",
		Check:     kubectl + check.String() + fmt.Sprintf(`echo "%s"`, syncedSentence(apps)),
		Do:        kubectl + wait.String(),
		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: time.Duration(len(apps))*o.timeout() + time.Minute,
		Attempts:  1,
	}
}

func syncedSentence(apps []Application) string {
	if len(apps) == 1 {
		return apps[0].Name + " is synced and healthy"
	}
	return strings.Join(names(apps), ", ") + " are synced and healthy"
}

// ---------------------------------------------------------------------------
// Manifests
// ---------------------------------------------------------------------------

// Chart renders the HelmChart RKE2 deploys.
func Chart(spec v1alpha1.ClusterSpec, o Options) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString(`apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: ` + yamlString(ReleaseName) + `
  namespace: kube-system
spec:
` + o.chartSource(spec, "argo-cd") + `  version: ` + yamlString(ChartVersion) + `
  targetNamespace: ` + yamlString(Namespace) + `
  createNamespace: true
  valuesContent: |-
    # The chart names its objects <release>-<chart>-<component> unless told
    # otherwise, which would give "argocd-argo-cd-server" -- a name that appears
    # in no ArgoCD runbook. The override produces the names the upstream install
    # manifest uses, so an operator following the documentation finds them.
    fullnameOverride: argocd
    crds:
      install: true
      keep: true
    dex:
      # This tool has no way to configure SSO, so Dex would be a pod that can
      # never do anything and an image an airgap bundle would have to carry for
      # it. Re-enabling it is a values change on this chart.
      enabled: false
`)

	// A private registry has to be told to every chart, or the pull fails with
	// an opaque error that names an upstream host nobody configured.
	if r := strings.TrimSpace(spec.Registry.SystemDefaultRegistry); r != "" &&
		spec.Registry.Mode != v1alpha1.RegistryEmbedded {
		b.WriteString("    global:\n      image:\n        repository: " +
			yamlString(r+"/argoproj/argocd") + "\n")
		b.WriteString("    redis:\n      image:\n        repository: " +
			yamlString(r+"/docker/library/redis") + "\n")
	}
	return b.String()
}

// Application is one bootstrap Application and the name to look for.
type Application struct {
	Name     string
	Manifest string
}

// Applications renders the Applications the document asks to be applied once.
//
// Everything after these is ArgoCD's: the schema calls them bootstrap apps
// because the engine applies them and then stops being involved.
func Applications(spec v1alpha1.ClusterSpec) ([]Application, error) {
	g := spec.Platform.GitOps
	if len(g.BootstrapApps) == 0 {
		return nil, nil
	}
	repo := repoURL(g)
	if repo == "" {
		return nil, fmt.Errorf(
			"platform.gitops.bootstrapApps names %d application(s) and no repository is configured; "+
				"set platform.gitops.%s", len(g.BootstrapApps), repoField(g))
	}

	out := make([]Application, 0, len(g.BootstrapApps))
	for _, entry := range g.BootstrapApps {
		app, err := application(g, repo, strings.TrimSpace(entry))
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, nil
}

// application renders one Application.
//
// The entry is a path in the repository for a Git source and `chart@version`
// for an OCI one. The version is required there and not defaulted to the
// newest: an unpinned chart means the bundle that crossed the air gap and the
// cluster disagree about what is installed, which is the failure every pinned
// version in this package exists to prevent.
func application(g v1alpha1.GitOpsSpec, repo, entry string) (Application, error) {
	if entry == "" {
		return Application{}, fmt.Errorf("platform.gitops.bootstrapApps has an empty entry")
	}

	var source, name string
	if g.Source == v1alpha1.GitOpsOCI {
		chart, version, ok := strings.Cut(entry, "@")
		if !ok || strings.TrimSpace(version) == "" {
			return Application{}, fmt.Errorf(
				"bootstrap app %q names no chart version; write it as %s@<version>, "+
					"because an unpinned chart means the airgap bundle and the cluster "+
					"disagree about what is installed", entry, entry)
		}
		name = appName(chart)
		source = "    chart: " + yamlString(chart) + "\n" +
			"    targetRevision: " + yamlString(version) + "\n"
	} else {
		branch := strings.TrimSpace(g.Branch)
		if branch == "" {
			branch = "HEAD"
		}
		name = appName(entry)
		source = "    path: " + yamlString(entry) + "\n" +
			"    targetRevision: " + yamlString(branch) + "\n"
	}

	// No resources-finalizer: deleting an Application would then delete
	// everything it deployed. An operator removing a bootstrap entry from the
	// document is saying this tool should stop managing the app, which is not
	// the same sentence as "delete the workload".
	manifest := managedFileHeader + `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ` + yamlString(name) + `
  namespace: ` + yamlString(Namespace) + `
spec:
  project: "default"
  source:
    repoURL: ` + yamlString(repo) + `
` + source + `  destination:
    server: "https://kubernetes.default.svc"
    namespace: ` + yamlString(name) + `
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
`
	return Application{Name: name, Manifest: manifest}, nil
}

// appsManifest joins the Applications into the one file the phase writes.
func appsManifest(apps []Application) string {
	parts := make([]string, 0, len(apps))
	for _, a := range apps {
		parts = append(parts, a.Manifest)
	}
	return strings.Join(parts, "---\n")
}

// probeApplication is the object the readiness check dry-runs when the document
// names no bootstrap app.
//
// It is never created. The server-side dry run goes through the API server, the
// CRD registration and any admission webhook and then discards the result,
// which is the whole path an Application has to survive.
func probeApplication() string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: "malmok-probe"
  namespace: ` + yamlString(Namespace) + `
spec:
  project: "default"
  source:
    repoURL: "https://example.invalid/probe.git"
    path: "."
    targetRevision: "HEAD"
  destination:
    server: "https://kubernetes.default.svc"
    namespace: "default"
`
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// credentialsFor returns the registry credential when the repository is hosted
// on the private registry, and nothing otherwise.
//
// The host has to match. Sending Harbor's password to github.com because both
// happen to be configured in the same document is how a credential leaves the
// place it was meant for.
func (m Material) credentialsFor(repo string) (user, pass string) {
	host := strings.TrimSpace(m.RegistryHost)
	if host == "" || m.RegistryUser == "" {
		return "", ""
	}
	if !sameHost(repo, host) {
		return "", ""
	}
	return m.RegistryUser, m.RegistryPass
}

// sameHost compares the host of a repository URL with a registry host.
//
// A repository may be written with a scheme or without one -- ArgoCD wants an
// OCI repository as a bare host and path -- so both forms have to reach the
// same answer.
func sameHost(repo, registry string) bool {
	return hostOf(repo) == hostOf(registry)
}

func hostOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "scheme://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// appName turns a repository path or chart name into an Application name.
//
// The last segment, because a path like `clusters/homelab/guestbook` names the
// app at its end and an Application called `clusters-homelab-guestbook` is one
// nobody types twice.
func appName(entry string) string {
	entry = strings.Trim(strings.TrimSpace(entry), "/")
	if i := strings.LastIndex(entry, "/"); i >= 0 {
		entry = entry[i+1:]
	}
	if entry == "" || entry == "." {
		return "root"
	}
	return entry
}

func names(apps []Application) []string {
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		out = append(out, a.Name)
	}
	return out
}

// repoField names the field the operator has to fill in, in their own document.
func repoField(g v1alpha1.GitOpsSpec) string {
	if g.Source == v1alpha1.GitOpsOCI {
		return "ociRepo"
	}
	return "gitRepo"
}

// jsonPathKey escapes the dots in an annotation name so jsonpath reads it as
// one key rather than as a path.
func jsonPathKey(s string) string {
	return strings.ReplaceAll(s, ".", `\.`)
}

// fingerprint is what the check compares, so a rotated credential is noticed
// without the password ever being read back off the cluster.
func fingerprint(user, pass string) string {
	sum := sha256.Sum256([]byte(user + "\x00" + pass))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
