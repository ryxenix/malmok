// Package rke2 builds the L1 phases: installing RKE2 and starting it.
//
// The install method is the tarball (ADR-013). RKE2's own install.sh with
// INSTALL_RKE2_METHOD=tar does the unpacking; what this package owns is the
// observable target on either side of it -- "this version of the binary is
// present", "this config file matches the document", "the node is Ready" --
// because docs/11-execute.md §3.1 refuses a step that cannot be observed. That
// is precisely why `curl -sfL https://get.rke2.io | sh` is not a step: it has
// to be restated as a target somebody can look at afterwards.
package rke2

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
)

// PhaseBootstrap is the phase that brings up the first server.
const PhaseBootstrap = "l1-bootstrap"

// Paths RKE2 uses. They are constants rather than options because RKE2's own
// units and its uninstall script assume them, and moving them means owning
// every consequence of that for the life of the cluster.
const (
	ConfigDir    = "/etc/rancher/rke2"
	ConfigFile   = ConfigDir + "/config.yaml"
	DataDir      = "/var/lib/rancher/rke2"
	BinDir       = DataDir + "/bin"
	Kubeconfig   = ConfigDir + "/rke2.yaml"
	TokenFile    = DataDir + "/server/node-token"
	InstallerURL = "https://get.rke2.io"
	// ImagesDir is where an airgapped install expects the image tarballs.
	ImagesDir = DataDir + "/agent/images"
)

// managedFileHeader marks the config file as ours.
const managedFileHeader = "# Managed by malmok. Changes here are overwritten on the next apply."

// Options are what the L1 steps need beyond the document.
type Options struct {
	// ArtifactPath is a directory holding the release artifacts, for an
	// airgapped install. Empty means the installer fetches them.
	ArtifactPath string

	// InstallTimeout bounds the install, which downloads a few hundred
	// megabytes on a first run.
	InstallTimeout time.Duration
	// ReadyTimeout bounds waiting for the node to report Ready. The first start
	// unpacks and starts every static pod, which is minutes rather than
	// seconds on a cold node.
	ReadyTimeout time.Duration

	// Prestage is written into the manifest directory before rke2-server first
	// starts. It exists for the configuration the cluster cannot come up
	// without: with kube-proxy disabled, Cilium must be told the API server's
	// direct address at bootstrap, or it waits on the in-cluster service IP
	// that only a running kube-proxy -- or a running Cilium -- would route.
	// Found live on the first IDC install: the node sat NotReady for 900s
	// while the config that would have fixed it waited in the next phase.
	Prestage []PrestagedManifest
}

// PrestagedManifest is one file for the manifest directory, written before
// the service starts so RKE2's own reconciler applies it on first boot.
type PrestagedManifest struct {
	Name string // step name
	Path string
	Body string
}

func (o Options) installTimeout() time.Duration {
	if o.InstallTimeout <= 0 {
		return 15 * time.Minute
	}
	return o.InstallTimeout
}

func (o Options) readyTimeout() time.Duration {
	if o.ReadyTimeout <= 0 {
		return 10 * time.Minute
	}
	return o.ReadyTimeout
}

// BootstrapSteps brings up the first server node.
//
// Order is forced: the binary has to exist before a config file means anything,
// and the config has to be in place before the service starts -- RKE2 reads it
// once at startup, so writing it afterwards would need a restart nobody asked
// for.
func BootstrapSteps(runner exec.Runner, node v1alpha1.NodeSpec, spec v1alpha1.ClusterSpec, o Options) []engine.Step {
	host := node.Host
	add := func(s *engine.ShellStep) engine.Step {
		s.Phase, s.Runner, s.Host = PhaseBootstrap, runner, host
		return s
	}
	steps := []engine.Step{
		add(installStep(spec.Kubernetes.Version, "server", o)),
		add(configStep(ServerConfig(node, spec, ""))),
	}
	for _, m := range o.Prestage {
		steps = append(steps, add(prestageStep(m)))
	}
	steps = append(steps,
		add(serviceStep("rke2-server", o)),
		// The operator's own access, not only the tool's: the account this
		// logged in as is the account kubectl and k9s will run from.
		add(kubeconfigStep(node.SSH.User)),
		// And the tools that access is for. A cluster whose kubeconfig is in
		// place but whose kubectl is buried in /var/lib/rancher looks broken
		// from the machine it was built on.
		add(opsToolsStep(spec.Network.Mode == v1alpha1.NetworkOnline)),
	)
	return steps
}

// prestageStep writes one manifest before the service starts. Write-only:
// there is no cluster to apply anything to yet, and RKE2 reconciles the
// directory on its first boot.
func prestageStep(m PrestagedManifest) *engine.ShellStep {
	return &engine.ShellStep{
		Name: m.Name,
		Check: fmt.Sprintf(`[ -f %s ] || { echo "%s does not exist"; exit 1; }
printf '%%s' %s | cmp -s - %s || { echo "%s differs from the document"; exit 1; }
echo "%s matches the document"`, m.Path, m.Path, ShellQuote(m.Body), m.Path, m.Path, m.Path),
		Do: fmt.Sprintf(`set -e
install -d -m 0755 %s
printf '%%s' %s > %s`, ManifestDir, ShellQuote(m.Body), m.Path),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// opsToolsStep puts kubectl, helm and k9s on the operator's PATH.
//
// RKE2 ships kubectl but buries it in /var/lib/rancher/rke2/bin, which is on
// nobody's PATH; the kubeconfig step gave the operator credentials to a
// cluster they then could not address. helm is not shipped at all -- RKE2
// bundles the helm *controller*, which reconciles HelmChart resources, and an
// operator who wants to look at what is installed or add a chart by hand needs
// the CLI. Both it and k9s come from a release page, so they are skipped
// off-line: an airgapped site gets them from the bundle or not at all, and a
// step that needs the internet must say so rather than hang.
func opsToolsStep(online bool) *engine.ShellStep {
	toolCheck := `command -v helm >/dev/null || { echo "helm is not installed"; exit 1; }
command -v k9s >/dev/null || { echo "k9s is not installed"; exit 1; }`
	toolDo := ""
	if online {
		toolDo = `
if ! command -v k9s >/dev/null; then
  arch=$(uname -m)
  case "$arch" in
    x86_64) a=amd64 ;;
    aarch64) a=arm64 ;;
    *) echo "no k9s build for $arch"; a="" ;;
  esac
  if [ -n "$a" ]; then
    curl -sfL --retry 3 --retry-delay 2 "https://github.com/derailed/k9s/releases/latest/download/k9s_Linux_${a}.tar.gz" | tar -xz -C /usr/local/bin k9s || { echo "could not fetch k9s (exit $?)"; exit 1; }
    chmod 0755 /usr/local/bin/k9s
  fi
fi
if ! command -v helm >/dev/null; then
  arch=$(uname -m)
  case "$arch" in
    x86_64) a=amd64 ;;
    aarch64) a=arm64 ;;
    *) echo "no helm build for $arch"; a="" ;;
  esac
  if [ -n "$a" ]; then
    # The version comes from helm's own pointer file rather than a number
    # pinned here, which would age into an install of something years old.
    # Assigned with a fallback because a bare command substitution that fails
    # under set -e takes the whole script with it -- the defect that once left
    # a wait loop in this package running zero times.
    v=$(curl -sfL --retry 3 --retry-delay 2 https://get.helm.sh/helm-latest-version || echo "")
    v=$(echo "$v" | tr -d '\r\n')
    if [ -z "$v" ]; then echo "could not ask helm which version is current"; exit 1; fi
    curl -sfL --retry 3 --retry-delay 2 "https://get.helm.sh/helm-${v}-linux-${a}.tar.gz" \
      | tar -xz -C /usr/local/bin --strip-components=1 "linux-${a}/helm" || { echo "could not fetch helm ${v} (exit $?)"; exit 1; }
    chmod 0755 /usr/local/bin/helm
  fi
fi`
	} else {
		// Off-line the check asks only for kubectl; reporting a missing helm
		// or k9s forever on a site that cannot fetch them is a step that never
		// settles.
		toolCheck = `true`
	}
	return &engine.ShellStep{
		Name: "operator-tools",
		Check: fmt.Sprintf(`command -v kubectl >/dev/null || { echo "kubectl is not on the PATH"; exit 1; }
%s
echo "kubectl $(kubectl version --client 2>/dev/null | head -1); $(helm version --short 2>/dev/null || echo 'helm absent'); $(k9s version -s 2>/dev/null | head -1 || echo 'k9s absent')"`, toolCheck),
		Do: fmt.Sprintf(`set -e
ln -sf %s/bin/kubectl /usr/local/bin/kubectl%s`, DataDir, toolDo),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// InstallStep puts RKE2 on a node at a given version.
//
// Exported because the upgrade phase needs exactly this step at a new version.
// ADR-013 made the observable "what does `rke2 --version` answer", which is
// what makes installing and upgrading one operation rather than two that have
// to be kept in agreement.
func InstallStep(version, kind string, o Options) *engine.ShellStep {
	return installStep(version, kind, o)
}

// installStep puts the RKE2 binary on the node at the requested version.
//
// The observable target is the version the binary reports. That makes the step
// re-runnable, makes an upgrade the same operation as an install, and means a
// half-finished extraction is detected rather than assumed away.
func installStep(version, kind string, o Options) *engine.ShellStep {
	if version == "" {
		version = "latest"
	}

	// The installer is invoked with the environment RKE2 documents. tar is the
	// method (ADR-013): one artifact set that an airgap bundle can carry, and
	// no package manager on the node deciding what happens.
	env := []string{
		"INSTALL_RKE2_METHOD=tar",
		"INSTALL_RKE2_TYPE=" + kind,
	}
	if version != "latest" {
		env = append(env, "INSTALL_RKE2_VERSION="+shellQuote(version))
	}

	var fetch string
	if o.ArtifactPath != "" {
		// An airgapped install reads the artifacts from disk. The installer
		// still verifies them against the checksum file beside them.
		env = append(env, "INSTALL_RKE2_ARTIFACT_PATH="+shellQuote(o.ArtifactPath))
		// Readable, not executable: the line below runs it with sh, and the
		// way anyone actually obtains this file -- curl -o install.sh
		// https://get.rke2.io -- leaves it mode 644. Demanding +x failed every
		// air-gapped install on a file that was present, correct, and about to
		// be run by an interpreter that does not care.
		fetch = fmt.Sprintf(`[ -r %s/install.sh ] || { echo "no readable install.sh in the artifact path"; exit 1; }
sh %s/install.sh`, shellQuote(o.ArtifactPath), shellQuote(o.ArtifactPath))
	} else {
		fetch = fmt.Sprintf(`curl -sfL %s -o /tmp/rke2-install.sh
sh /tmp/rke2-install.sh
rm -f /tmp/rke2-install.sh`, InstallerURL)
	}

	return &engine.ShellStep{
		Name: "install",
		Check: fmt.Sprintf(`command -v rke2 >/dev/null 2>&1 || { echo "rke2 is not installed"; exit 1; }
have=$(rke2 --version 2>/dev/null | head -1 | awk '{print $3}' || true)
[ -n "$have" ] || { echo "rke2 is present but reports no version"; exit 1; }
want=%s
if [ "$want" != latest ] && [ "$have" != "$want" ]; then
  echo "rke2 is $have, the document asks for $want"; exit 1
fi
echo "rke2 $have"`, shellQuote(version)),
		Do:        "set -e\nexport " + strings.Join(env, " ") + "\n" + fetch,
		Satisfied: "%s",
		Missing:   "%s",
		// A download that failed halfway is worth another go; a version that
		// does not exist is not, and that shows up as the installer refusing.
		Attempts:  3,
		DoTimeout: o.installTimeout(),
	}
}

// configStep writes config.yaml.
//
// The check compares content rather than presence: a config file left from an
// earlier document produces a cluster configured for something nobody asked
// for, and RKE2 reads it only at startup so the mismatch is silent until the
// next restart.
func configStep(body string) *engine.ShellStep {
	return &engine.ShellStep{
		Name: "config",
		Check: fmt.Sprintf(`[ -f %s ] || { echo "%s does not exist"; exit 1; }
printf '%%s' %s | cmp -s - %s || { echo "%s differs from the document"; exit 1; }
echo "%s matches the document"`,
			ConfigFile, ConfigFile, shellQuote(body), ConfigFile, ConfigFile, ConfigFile),
		// The file carries the cluster token, so it is written 0600 and its
		// contents never reach the event stream.
		Do: fmt.Sprintf(`set -e
install -d -m 0755 %s
umask 077
printf '%%s' %s > %s
chmod 0600 %s`, ConfigDir, shellQuote(body), ConfigFile, ConfigFile),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// serviceStep starts the unit and waits until the node reports Ready.
//
// "Started" is not the target. systemd reports a unit as active the moment the
// process is up, which on a first start is several minutes before the API
// server answers and the node registers -- and a phase that moved on at that
// point would try to join a second server to something that cannot accept it.
func serviceStep(unit string, o Options) *engine.ShellStep {
	ready := fmt.Sprintf(`export PATH=$PATH:%s
export KUBECONFIG=%s
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"="}{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{"\n"}{end}' 2>/dev/null`,
		BinDir, Kubeconfig)

	return &engine.ShellStep{
		Name: "service",
		Check: fmt.Sprintf(`systemctl is-active --quiet %s || { echo "%s is not running"; exit 1; }
%s
[ -f %s ] || { echo "%s is running and has not written a kubeconfig yet"; exit 1; }
out=$(%s)
echo "$out" | grep -q '=True$' || { echo "%s is running but no node is Ready yet"; exit 1; }
echo "$(echo "$out" | grep -c '=True$') node(s) Ready"`,
			unit, unit, configIsOlderThanProcess(unit), Kubeconfig, unit, ready, unit),

		Do: fmt.Sprintf(`set -e
if systemctl is-active --quiet %s; then
  # Already running: what is unsatisfied is the configuration it was started
  # with, so restarting is the whole of the work.
  systemctl restart %s
else
  systemctl enable --now %s
fi
deadline=$(( $(date +%%s) + %d ))
started=$(date +%%s)
said=0
while [ "$(date +%%s)" -lt "$deadline" ]; do
  if [ -f %s ] && %s | grep -q '=True$'; then exit 0; fi
  if ! systemctl is-active --quiet %s; then
    echo "%s stopped while starting:"; journalctl -u %s -n 30 --no-pager 2>&1 | tail -30; exit 1
  fi
  # Every fifteen seconds, where it has got to. A first start unpacks a few
  # hundred megabytes and brings up five static pods, which is minutes of
  # silence otherwise -- and silence is indistinguishable from a hang.
  now=$(date +%%s)
  if [ $(( now - said )) -ge 15 ]; then
    said=$now
    pods=$(ctr -a /run/k3s/containerd/containerd.sock -n k8s.io c ls 2>/dev/null | grep -c . || echo 0)
    node=no-kubeconfig-yet
    [ -f %s ] && node=$(%s | tr '\n' ' ')
    echo "waiting $(( now - started ))s: $pods container(s), node: ${node:-not registered}"
  fi
  sleep 5
done
echo "%s did not report a Ready node within %ds:"
journalctl -u %s -n 40 --no-pager 2>&1 | tail -40
exit 1`,
			unit, unit, unit, int(o.readyTimeout().Seconds()),
			Kubeconfig, ready, unit, unit, unit,
			Kubeconfig, ready,
			unit, int(o.readyTimeout().Seconds()), unit),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.readyTimeout() + time.Minute,
	}
}

// configIsOlderThanProcess refuses to call a service satisfied when it is
// running with a configuration older than the file on disk.
//
// RKE2 reads config.yaml once, at startup. Without this the config step writes
// a change, the service step sees a running unit and a Ready node, and the
// change never takes effect -- which is how a cluster ends up with a
// certificate that does not cover the VIP the document asked for, while every
// step reports success.
func configIsOlderThanProcess(unit string) string {
	return fmt.Sprintf(`started=$(systemctl show %s -p ExecMainStartTimestamp --value)
if [ -n "$started" ] && [ -f %s ]; then
  s=$(date -d "$started" +%%s 2>/dev/null || echo 0)
  c=$(stat -c %%Y %s 2>/dev/null || echo 0)
  if [ "$s" -gt 0 ] && [ "$c" -gt "$s" ]; then
    echo "%s is running with a configuration older than %s; it reads that file only at startup"
    exit 1
  fi
fi`, unit, ConfigFile, ConfigFile, unit, ConfigFile)
}

// ---------------------------------------------------------------------------
// config.yaml
// ---------------------------------------------------------------------------

// ServerConfig renders config.yaml for a server node.
//
// token is empty for the first server: RKE2 generates one and writes it to
// TokenFile, which is where the join phases read it from. Putting a token in
// cluster.yaml would be a plaintext secret in an artifact handed to customers.
func ServerConfig(node v1alpha1.NodeSpec, spec v1alpha1.ClusterSpec, token string) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")

	if token != "" {
		b.WriteString("token: " + yamlString(token) + "\n")
	}

	// Every address a client might use to reach the API has to be in the
	// certificate before it is issued. Adding one later means regenerating the
	// certificates on every server, which is the thing HA promotion trips over.
	if sans := tlsSANs(node, spec); len(sans) > 0 {
		b.WriteString("tls-san:\n")
		for _, san := range sans {
			b.WriteString("  - " + yamlString(san) + "\n")
		}
	}

	// The advertised address is the document's, not the default route's.
	// Unpinned, the kubelet advertises whichever interface holds the default
	// route -- on the first IDC node that was the public one, and the operator
	// had named the internal address in the document all along.
	ip := strings.TrimSpace(node.NodeIP)
	if ip == "" && net.ParseIP(strings.TrimSpace(node.Host)) != nil {
		ip = strings.TrimSpace(node.Host)
	}
	if ip != "" {
		b.WriteString("node-ip: " + yamlString(ip) + "\n")
	}
	if h := strings.TrimSpace(node.Hostname); h != "" {
		b.WriteString("node-name: " + yamlString(h) + "\n")
	}

	if cni := cniFor(spec.Kubernetes.Dataplane.Preset); cni != "" {
		b.WriteString("cni: " + yamlString(cni) + "\n")
	}
	if DisablesKubeProxy(spec) {
		b.WriteString("disable-kube-proxy: true\n")
	}

	// ADR-005: rke2-ingress-nginx is always disabled. It reached EOL in March
	// 2026 and receives no security patches; the document may list more.
	b.WriteString("disable:\n")
	for _, d := range disabled(spec) {
		b.WriteString("  - " + yamlString(d) + "\n")
	}

	if r := strings.TrimSpace(spec.Registry.SystemDefaultRegistry); r != "" &&
		spec.Registry.Mode != v1alpha1.RegistryEmbedded {
		b.WriteString("system-default-registry: " + yamlString(r) + "\n")
	}

	writeList(&b, "kubelet-arg", spec.Kubernetes.KubeletArgs)
	writeList(&b, "kube-apiserver-arg", spec.Kubernetes.APIServerArgs)
	return b.String()
}

// writeList emits a YAML sequence under one key. Repeating the key per item
// would be a different document: the last one wins and the rest are dropped.
func writeList(b *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		return
	}
	b.WriteString(key + ":\n")
	for _, v := range values {
		b.WriteString("  - " + yamlString(v) + "\n")
	}
}

// DisablesKubeProxy reports whether the dataplane replaces kube-proxy.
//
// ADR-004 binds the CNI, the Gateway controller and the load balancer into one
// choice, and this is one of the places that binding is not optional: Cilium's
// Gateway API support requires kube-proxy replacement, and leaving RKE2's own
// kube-proxy running alongside it means two things programming the same service
// rules. The symptom is intermittent and looks like a network fault.
//
// It belongs to the L1 configuration rather than to l2-dataplane because RKE2
// reads it at startup, which is before any of L2 exists.
func DisablesKubeProxy(spec v1alpha1.ClusterSpec) bool {
	return strings.HasPrefix(string(spec.Kubernetes.Dataplane.Preset), "cilium")
}

// tlsSANs is every name the API certificate has to cover.
//
// The registration address is always included: it is what every node joins
// through, and a certificate that does not cover it fails every join with a
// TLS error that names the wrong thing.
func tlsSANs(node v1alpha1.NodeSpec, spec v1alpha1.ClusterSpec) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	add(spec.Topology.RegistrationAddress)
	if v := spec.Topology.VIP; v != nil {
		add(v.Address)
	}
	for _, s := range spec.Topology.TLSSAN {
		add(s)
	}
	// Future servers have to be covered now. Adding a SAN later regenerates the
	// certificates on every existing server, which is exactly what §TLSSAN in
	// the schema warns about.
	for _, n := range spec.Topology.Servers {
		add(n.Host)
		add(n.NodeIP)
		add(n.Hostname)
	}
	add(node.Host)
	return out
}

// disabled is the bundled component list, with the always-off one first.
func disabled(spec v1alpha1.ClusterSpec) []string {
	seen := map[string]bool{"rke2-ingress-nginx": true}
	out := []string{"rke2-ingress-nginx"}

	// RKE2 v1.36 replaced the EOL'd ingress-nginx with a bundled Traefik --
	// the succession ADR-005 predicted, under a new name the disable list did
	// not cover. On the preset whose gateway is Cilium it has to go for two
	// reasons: two gateway controllers fight over the same Gateways (ADR-004
	// binds the controller to the preset), and its CRD chart tries to take
	// Helm ownership of the Gateway API CRDs this tool already installed --
	// which fails with "exists and cannot be imported", leaving two install
	// jobs crash-looping forever. Found live after the 1.36 upgrade.
	//
	// The *-traefik presets keep it: there, the bundled Traefik is the gateway.
	// On versions that ship no such component the entry matches nothing, which
	// is what makes it safe to state unconditionally for the preset.
	// Both names: the CRD chart is its own component, and disabling only the
	// chart that consumes it leaves the CRD installer crash-looping alone --
	// verified live, where `disable: rke2-traefik` removed one of the two.
	if spec.Kubernetes.Dataplane.Preset == v1alpha1.DataplaneCiliumGW {
		for _, d := range []string{"rke2-traefik", "rke2-traefik-crd"} {
			seen[d] = true
			out = append(out, d)
		}
	}

	for _, d := range spec.Kubernetes.DisableBundled {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// cniFor maps a dataplane preset onto what RKE2 calls the CNI.
//
// ADR-004 binds CNI, Gateway and load balancer together, so the preset is the
// only input; picking a CNI independently is what produces a cluster with a
// gateway nothing can give an address to.
func cniFor(preset v1alpha1.DataplanePreset) string {
	switch {
	case strings.HasPrefix(string(preset), "cilium"):
		return "cilium"
	case strings.HasPrefix(string(preset), "canal"):
		return "canal"
	case preset == "":
		return ""
	}
	return string(preset)
}

// yamlString quotes a value so punctuation cannot change the meaning of the
// document. A token or a registry host containing a colon would otherwise
// silently become something else.
func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// shellQuote wraps a value for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// kubeconfigStep puts the cluster's kubeconfig where the operator's own tools
// look for it.
//
// RKE2 writes /etc/rancher/rke2/rke2.yaml root-only, which is correct for the
// file and useless for the person: the account this tool logged in as -- the
// account somebody will run kubectl or k9s from five minutes after the install
// finishes -- cannot read it, and the cluster looks broken from the very
// machine it was built on. The copy is theirs: owned by them, mode 600, at the
// path every kubernetes client checks first.
//
// A copy rather than a symlink or a group, because the alternatives change the
// security of the original: a symlink needs the source readable, and a group
// puts every future member one `usermod` away from cluster-admin. The copy can
// go stale only if the cluster CA rotates, and the check compares content so a
// re-run repairs exactly that.
func kubeconfigStep(user string) *engine.ShellStep {
	// The account is decided at run time, not at render time: over SSH it is
	// the login account, and on a local node -- where the tool runs under sudo
	// and the document names no credentials -- it is whoever sudo elevated.
	// Root itself needs no copy; the original is already root's to read.
	resolve := fmt.Sprintf(`u=%s
[ -n "$u" ] || u=$SUDO_USER
[ -n "$u" ] && [ "$u" != root ] || { echo "the operator is root, who can read the original"; exit 0; }
home=$(getent passwd "$u" | cut -d: -f6)
[ -n "$home" ] || { echo "no home directory for $u"; exit 1; }`, shellQuote(user))

	return &engine.ShellStep{
		Name: "kubeconfig",
		Check: resolve + fmt.Sprintf(`
cmp -s %s "$home/.kube/config" || { echo "$u has no current kubeconfig"; exit 1; }
owner=$(stat -c %%U "$home/.kube/config")
[ "$owner" = "$u" ] || { echo "$home/.kube/config belongs to $owner, not $u"; exit 1; }
echo "$u can reach the cluster from $home/.kube/config"`, Kubeconfig),

		Do: resolve + fmt.Sprintf(`
set -e
install -d -m 700 -o "$u" -g "$(id -gn "$u")" "$home/.kube"
install -m 600 -o "$u" -g "$(id -gn "$u")" %s "$home/.kube/config"`, Kubeconfig),

		Satisfied: "%s",
		Missing:   "%s",
	}
}
