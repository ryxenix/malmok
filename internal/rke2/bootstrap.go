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
	"strings"
	"time"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/exec"
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
const managedFileHeader = "# Managed by platformctl. Changes here are overwritten on the next apply."

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
	return []engine.Step{
		add(installStep(spec.Kubernetes.Version, "server", o)),
		add(configStep(ServerConfig(node, spec, ""))),
		add(serviceStep("rke2-server", o)),
	}
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
		fetch = fmt.Sprintf(`[ -x %s/install.sh ] || { echo "no install.sh in the artifact path"; exit 1; }
sh %s/install.sh`, shellQuote(o.ArtifactPath), shellQuote(o.ArtifactPath))
	} else {
		fetch = fmt.Sprintf(`curl -sfL %s -o /tmp/rke2-install.sh
sh /tmp/rke2-install.sh
rm -f /tmp/rke2-install.sh`, InstallerURL)
	}

	return &engine.ShellStep{
		Name: "install",
		Check: fmt.Sprintf(`command -v rke2 >/dev/null 2>&1 || { echo "rke2 is not installed"; exit 1; }
have=$(rke2 --version 2>/dev/null | head -1 | awk '{print $3}')
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
[ -f %s ] || { echo "%s is running and has not written a kubeconfig yet"; exit 1; }
out=$(%s)
echo "$out" | grep -q '=True$' || { echo "%s is running but no node is Ready yet"; exit 1; }
echo "$(echo "$out" | grep -c '=True$') node(s) Ready"`,
			unit, unit, Kubeconfig, unit, ready, unit),

		Do: fmt.Sprintf(`set -e
systemctl enable --now %s
deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$deadline" ]; do
  if [ -f %s ] && %s | grep -q '=True$'; then exit 0; fi
  if ! systemctl is-active --quiet %s; then
    echo "%s stopped while starting:"; journalctl -u %s -n 30 --no-pager 2>&1 | tail -30; exit 1
  fi
  sleep 5
done
echo "%s did not report a Ready node within %ds:"
journalctl -u %s -n 40 --no-pager 2>&1 | tail -40
exit 1`,
			unit, int(o.readyTimeout().Seconds()),
			Kubeconfig, ready, unit, unit, unit, unit, int(o.readyTimeout().Seconds()), unit),

		Satisfied: "%s",
		Missing:   "%s",
		DoTimeout: o.readyTimeout() + time.Minute,
	}
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

	if ip := strings.TrimSpace(node.NodeIP); ip != "" {
		b.WriteString("node-ip: " + yamlString(ip) + "\n")
	}
	if h := strings.TrimSpace(node.Hostname); h != "" {
		b.WriteString("node-name: " + yamlString(h) + "\n")
	}

	if cni := cniFor(spec.Kubernetes.Dataplane.Preset); cni != "" {
		b.WriteString("cni: " + yamlString(cni) + "\n")
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
