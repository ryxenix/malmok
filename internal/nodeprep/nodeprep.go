// Package nodeprep builds the l0-node-prep phase.
//
// ADR-012: there is no configuration management tool underneath this. The
// engine already owns idempotency, retry, resume and the event stream, so a
// step here is exactly what docs/11-execute.md §3.1 describes -- observe
// without changing anything, apply only when unsatisfied, observe again.
//
// The shape below is a check/do pair of shell programs, and it is deliberately
// the only shape. It is not a module layer: there is no package manager
// abstraction, no template engine and no file-editing primitive, because
// ADR-012 fixes the boundary at the concrete steps the catalogue asks for. A
// second caller has to exist before anything here is generalised.
package nodeprep

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/engine"
	"github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/rke2"
)

// Phase is the phase name these steps belong to.
const Phase = "l0-node-prep"

// ---------------------------------------------------------------------------
// The catalogue
// ---------------------------------------------------------------------------

// managedFileHeader marks every file this phase writes.
//
// A customer site six months later has to be able to tell what put a file
// there. Without it, an operator finds a sysctl they did not write and cannot
// tell whether removing it breaks something.
const managedFileHeader = "# Managed by malmok. Changes here are overwritten on the next apply."

// sysctls are what the kubelet and the dataplane need.
//
// bridge-nf-call-* are deliberately absent from the check: they do not exist
// until br_netfilter is loaded, and a check that fails on a node where the
// module has simply not been loaded yet would never be satisfiable. The file
// sets them; the module step is what makes them take effect.
var sysctls = []struct{ key, value string }{
	{"net.ipv4.ip_forward", "1"},
	{"net.ipv6.conf.all.forwarding", "1"},
	{"net.bridge.bridge-nf-call-iptables", "1"},
	{"net.bridge.bridge-nf-call-ip6tables", "1"},
	{"fs.inotify.max_user_instances", "8192"},
	{"fs.inotify.max_user_watches", "524288"},
}

// checkedSysctls are the ones observable without loading a module.
var checkedSysctls = []string{
	"net.ipv4.ip_forward",
	"net.ipv6.conf.all.forwarding",
	"fs.inotify.max_user_instances",
	"fs.inotify.max_user_watches",
}

// modules have to survive a reboot, which is why the file matters as much as
// the running state: a node that works until it is restarted is worse than one
// that fails now.
var modules = []string{"br_netfilter", "overlay"}

const sysctlFile = "/etc/sysctl.d/90-malmok.conf"
const modulesFile = "/etc/modules-load.d/90-malmok.conf"

// Steps returns the l0-node-prep catalogue for one node.
//
// Order matters in one place: the modules have to be loaded before the sysctls
// that live under them can be set, so the module step comes first.
func Steps(runner exec.Runner, host string, spec v1alpha1.ClusterSpec, trust TrustMaterial) []engine.Step {
	var out []engine.Step

	add := func(s *engine.ShellStep) {
		s.Phase, s.Runner, s.Host = Phase, runner, host
		out = append(out, s)
	}

	add(modulesStep())
	add(sysctlStep())
	// A document that keeps its swap gets no swap step. Note what this does
	// not do: it does not turn swap back on. The tool stops changing the
	// setting, it does not undo a change an earlier run made.
	if spec.OS.SwapDisabled() {
		add(swapStep())
	}
	add(dataDirStep())

	if len(trust.CABundle) > 0 {
		add(trustStep(trust))
	}
	if reg := registriesYAML(spec, trust); reg != "" {
		add(registriesStep(reg))
	}
	return out
}

// modulesStep loads the kernel modules and makes the choice survive a reboot.
func modulesStep() *engine.ShellStep {
	var loaded, wanted []string
	for _, m := range modules {
		loaded = append(loaded, fmt.Sprintf(`[ -d /sys/module/%s ] || { echo "%s not loaded"; exit 1; }`, m, m))
		wanted = append(wanted, m)
	}
	return &engine.ShellStep{
		Name: "modules",
		Check: fmt.Sprintf(`grep -qF %q %s 2>/dev/null || { echo "%s absent"; exit 1; }
%s
echo "%s loaded and configured"`,
			managedFileHeader, modulesFile, modulesFile,
			strings.Join(loaded, "\n"), strings.Join(wanted, ", ")),
		Do: fmt.Sprintf(`set -e
printf '%%s\n%s\n' %q > %s
for m in %s; do modprobe "$m"; done`,
			strings.Join(wanted, "\n"), managedFileHeader, modulesFile, strings.Join(wanted, " ")),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// sysctlStep writes the values and applies them.
func sysctlStep() *engine.ShellStep {
	var checks []string
	for _, k := range checkedSysctls {
		var want string
		for _, s := range sysctls {
			if s.key == k {
				want = s.value
			}
		}
		checks = append(checks, fmt.Sprintf(
			`v=$(sysctl -n %s 2>/dev/null || echo missing); [ "$v" = %q ] || { echo "%s is $v, want %s"; exit 1; }`,
			k, want, k, want))
	}

	var lines []string
	for _, s := range sysctls {
		lines = append(lines, s.key+" = "+s.value)
	}

	return &engine.ShellStep{
		Name: "sysctl",
		Check: fmt.Sprintf(`grep -qF %q %s 2>/dev/null || { echo "%s absent"; exit 1; }
%s
echo "every value is set"`, managedFileHeader, sysctlFile, sysctlFile, strings.Join(checks, "\n")),
		Do: fmt.Sprintf(`set -e
printf '%%s\n%s\n' %q > %s
sysctl --system >/dev/null`,
			strings.Join(lines, "\n"), managedFileHeader, sysctlFile),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// swapStep turns swap off and takes it out of fstab.
//
// Both halves matter and for different reasons: the kubelet refuses to start
// with swap active, and an fstab entry left behind brings it back at the next
// reboot -- which turns a working cluster into a broken one at the worst
// possible moment, months later, with nothing having visibly changed.
func swapStep() *engine.ShellStep {
	return &engine.ShellStep{
		Name: "swap",
		Check: `active=$(swapon --show=NAME --noheadings 2>/dev/null | tr '\n' ' ')
[ -z "$active" ] || { echo "swap is active: $active"; exit 1; }
if grep -qE '^[^#].*[[:space:]]swap[[:space:]]' /etc/fstab 2>/dev/null; then
  echo "swap is off but /etc/fstab would bring it back at the next reboot"; exit 1
fi
echo "swap is off and /etc/fstab has no entry"`,
		Do: `set -e
swapoff -a
if grep -qE '^[^#].*[[:space:]]swap[[:space:]]' /etc/fstab; then
  cp -a /etc/fstab /etc/fstab.malmok.bak
  sed -i -E 's|^([^#].*[[:space:]]swap[[:space:]].*)$|# malmok disabled swap: \1|' /etc/fstab
fi`,
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// dataDirStep creates the directory RKE2 grows into.
func dataDirStep() *engine.ShellStep {
	return &engine.ShellStep{
		Name: "datadir",
		Check: `[ -d /var/lib/rancher ] || { echo "/var/lib/rancher does not exist"; exit 1; }
echo "/var/lib/rancher exists"`,
		Do:        `install -d -m 0755 /var/lib/rancher`,
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// TrustMaterial is what the caller resolved for this node.
//
// It is passed in rather than read here because reading a SourceRef needs the
// document's directory and its secret policy, both of which belong to the
// loader.
type TrustMaterial struct {
	// CABundle is the private CA to install into the node trust store.
	CABundle []byte
	// RegistryHost and credentials configure containerd's mirror.
	RegistryHost string
	RegistryUser string
	RegistryPass string
	// RegistryCAPath is where the CA lands once the trust step has run.
	RegistryCAPath string
}

// caFile is where the private CA is written on the node.
//
// Both families read a directory of PEM files and rebuild a bundle from it, but
// they use different directories and different commands, which is the one place
// the family actually matters.
const (
	caFileDebian = "/usr/local/share/ca-certificates/malmok.crt"
	caFileRHEL   = "/etc/pki/ca-trust/source/anchors/malmok.crt"
)

// trustStep installs the private CA into the node trust store.
//
// docs/11-execute.md §2.1: this belongs to l0 rather than l2 because L1 pulls
// images before any of the cluster exists. Without it every pull fails with an
// opaque x509 error, which is the most common private-CA misinstall there is.
//
// Both halves quote the certificate. 0.88.0 fixed Do and left Check passing the
// PEM raw, which put its second line where the shell expected another command:
// the certificate was installed correctly and could never be confirmed, so
// every private-CA case halted on EX-003 -- applied, and the target state not
// reached. It is the same material in both, so it is quoted the same way in
// both.
func trustStep(t TrustMaterial) *engine.ShellStep {
	pem := string(t.CABundle)
	return &engine.ShellStep{
		Name: "ca-trust",
		Check: fmt.Sprintf(`f=%s; [ -d /etc/pki/ca-trust/source/anchors ] && f=%s
[ -f "$f" ] || { echo "the CA is not installed at $f"; exit 1; }
printf '%%s' %s | cmp -s - "$f" || { echo "the installed CA at $f differs from the document's"; exit 1; }
subject=$(openssl x509 -noout -subject -in "$f" 2>/dev/null || echo unknown)
echo "installed: $subject"`, caFileDebian, caFileRHEL, rke2.ShellQuote(pem)),
		Do: fmt.Sprintf(`set -e
if [ -d /etc/pki/ca-trust/source/anchors ]; then
  printf '%%s' %s > %s
  update-ca-trust extract
else
  install -d -m 0755 /usr/local/share/ca-certificates
  printf '%%s' %s > %s
  update-ca-certificates >/dev/null
fi`, rke2.ShellQuote(pem), caFileRHEL, rke2.ShellQuote(pem), caFileDebian),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

const registriesFile = "/etc/rancher/rke2/registries.yaml"

// registriesYAML renders containerd's mirror configuration, or "" when the
// document configures no external registry and no embedded one.
//
// Three separate things end up in this one file, and they are independent:
//
//   - the embedded mirror, which is a registry name with no endpoint under it;
//   - mirrors the document names, which are endpoints to try before upstream;
//   - the private registry a mode names, with its credentials and its CA.
//
// They used to be one decision, so naming a mirror without also naming a
// private registry wrote nothing at all.
func registriesYAML(spec v1alpha1.ClusterSpec, t TrustMaterial) string {
	embedded := spec.Registry.EmbeddedMirror()

	// host is the private registry, when a mode names one. It is what the
	// credentials and the CA belong to, and what "*" points at when the
	// document names no mirrors of its own.
	host := ""
	if spec.Registry.Mode != "" && !embedded {
		host = strings.TrimSpace(spec.Registry.SystemDefaultRegistry)
		if host == "" {
			host = t.RegistryHost
		}
	}

	// Sorted, because the step writes this file and then compares what it
	// finds against what it meant to write. Walking the map in its own order
	// produced a different file each run, which is drift the step caused
	// reporting as drift it found.
	upstreams := make([]string, 0, len(spec.Registry.Mirrors))
	for u := range spec.Registry.Mirrors {
		upstreams = append(upstreams, u)
	}
	sort.Strings(upstreams)

	if !embedded && host == "" && len(upstreams) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString("mirrors:\n")

	// A name under mirrors: with nothing under it is how RKE2 is told that a
	// registry takes part in the embedded mirror, and "*" is every registry --
	// which is what an air-gapped node wants, because images seeded from a
	// tarball are shared under whatever registry they are tagged for,
	// including one that does not exist.
	//
	// It assumes every node in the cluster is equally trusted: a peer can
	// fetch any image another peer holds without presenting the credentials
	// that image was originally pulled with. That is true of the profiles this
	// is the default for, and it is why the modes that name a private registry
	// do not use it.
	//
	// Endpoints and the embedded mirror are not alternatives. RKE2 tries the
	// embedded mirror first and the listed endpoints after it, so a cluster
	// can share what it already has and reach a cache for what it does not.
	if embedded && !contains(upstreams, "*") {
		b.WriteString("  \"*\":\n")
	}
	if host != "" && len(upstreams) == 0 {
		b.WriteString("  \"*\":\n    endpoint:\n      - \"https://" + host + "\"\n")
	}
	for _, upstream := range upstreams {
		b.WriteString("  \"" + upstream + "\":\n")
		endpoints := spec.Registry.Mirrors[upstream]
		if len(endpoints) == 0 {
			// Deliberate: a bare name is how a document adds one registry to
			// the embedded mirror without redirecting it anywhere.
			continue
		}
		b.WriteString("    endpoint:\n")
		for _, e := range endpoints {
			b.WriteString("      - \"" + endpointURL(e) + "\"\n")
		}
	}

	// configs describes the hosts a pull actually connects to, which is the
	// private registry AND every https mirror endpoint. Describing only the
	// first meant a mirror behind a private CA got no ca_file and failed with
	// an opaque x509 error on every pull.
	//
	// http endpoints need no entry: there is no certificate to verify.
	configured := make([]string, 0, len(upstreams)+1)
	if host != "" {
		configured = append(configured, host)
	}
	for _, upstream := range upstreams {
		for _, e := range spec.Registry.Mirrors[upstream] {
			u := endpointURL(e)
			if !strings.HasPrefix(u, "https://") {
				continue
			}
			if h := strings.TrimPrefix(u, "https://"); !contains(configured, h) {
				configured = append(configured, h)
			}
		}
	}
	sort.Strings(configured)
	if len(configured) == 0 {
		return b.String()
	}

	insecure := spec.Registry.Insecure != nil && *spec.Registry.Insecure
	b.WriteString("configs:\n")
	for _, h := range configured {
		b.WriteString("  \"" + h + "\":\n")
		// Credentials belong to the private registry the document named. A
		// mirror is somebody else's endpoint and sending them there would be
		// handing the registry's password to a cache.
		if h == host && (t.RegistryUser != "" || t.RegistryPass != "") {
			b.WriteString("    auth:\n")
			b.WriteString("      username: " + yamlString(t.RegistryUser) + "\n")
			b.WriteString("      password: " + yamlString(t.RegistryPass) + "\n")
		}
		b.WriteString("    tls:\n")
		switch {
		case insecure:
			b.WriteString("      insecure_skip_verify: true\n")
		case len(t.CABundle) > 0:
			ca := t.RegistryCAPath
			if ca == "" {
				ca = caFileDebian
			}
			b.WriteString("      ca_file: " + ca + "\n")
		default:
			b.WriteString("      insecure_skip_verify: false\n")
		}
	}
	return b.String()
}

// endpointURL gives an endpoint a scheme when the document left it off.
//
// https is the assumption, because a mirror without one is almost always a
// registry and not a LAN cache; a cache on plain http says so.
func endpointURL(e string) string {
	e = strings.TrimSpace(e)
	if strings.Contains(e, "://") {
		return e
	}
	return "https://" + e
}

// contains reports whether the slice holds the string.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// registriesStep writes containerd's mirror configuration.
//
// The file holds a registry password, so it is written 0600 and the check never
// prints its contents: a step's evidence goes into the audit report, and the
// report gets handed to the customer.
func registriesStep(body string) *engine.ShellStep {
	return &engine.ShellStep{
		Name: "registries",
		Check: fmt.Sprintf(`[ -f %s ] || { echo "%s does not exist"; exit 1; }
printf '%%s' %s | cmp -s - %s || { echo "%s differs from the document"; exit 1; }
echo "%s matches the document"`,
			registriesFile, registriesFile, rke2.ShellQuote(body), registriesFile, registriesFile, registriesFile),
		Do: fmt.Sprintf(`set -e
install -d -m 0755 /etc/rancher/rke2
umask 077
printf '%%s' %s > %s
chmod 0600 %s`, rke2.ShellQuote(body), registriesFile, registriesFile),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// yamlString quotes a value so a password containing a colon or a hash does not
// change the meaning of the document.
func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
