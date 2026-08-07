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
	"strings"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/exec"
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
const managedFileHeader = "# Managed by platformctl. Changes here are overwritten on the next apply."

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

const sysctlFile = "/etc/sysctl.d/90-platformctl.conf"
const modulesFile = "/etc/modules-load.d/90-platformctl.conf"

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
	add(swapStep())
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
  cp -a /etc/fstab /etc/fstab.platformctl.bak
  sed -i -E 's|^([^#].*[[:space:]]swap[[:space:]].*)$|# platformctl disabled swap: \1|' /etc/fstab
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
	caFileDebian = "/usr/local/share/ca-certificates/platformctl.crt"
	caFileRHEL   = "/etc/pki/ca-trust/source/anchors/platformctl.crt"
)

// trustStep installs the private CA into the node trust store.
//
// docs/11-execute.md §2.1: this belongs to l0 rather than l2 because L1 pulls
// images before any of the cluster exists. Without it every pull fails with an
// opaque x509 error, which is the most common private-CA misinstall there is.
func trustStep(t TrustMaterial) *engine.ShellStep {
	pem := string(t.CABundle)
	return &engine.ShellStep{
		Name: "ca-trust",
		Check: fmt.Sprintf(`f=%s; [ -d /etc/pki/ca-trust/source/anchors ] && f=%s
[ -f "$f" ] || { echo "the CA is not installed at $f"; exit 1; }
printf '%%s' %q | cmp -s - "$f" || { echo "the installed CA at $f differs from the document's"; exit 1; }
subject=$(openssl x509 -noout -subject -in "$f" 2>/dev/null || echo unknown)
echo "installed: $subject"`, caFileDebian, caFileRHEL, pem),
		Do: fmt.Sprintf(`set -e
if [ -d /etc/pki/ca-trust/source/anchors ]; then
  printf '%%s' %q > %s
  update-ca-trust extract
else
  install -d -m 0755 /usr/local/share/ca-certificates
  printf '%%s' %q > %s
  update-ca-certificates >/dev/null
fi`, pem, caFileRHEL, pem, caFileDebian),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

const registriesFile = "/etc/rancher/rke2/registries.yaml"

// registriesYAML renders containerd's mirror configuration, or "" when the
// document configures no external registry.
//
// The embedded mirror needs no file: it serves what the nodes already have.
func registriesYAML(spec v1alpha1.ClusterSpec, t TrustMaterial) string {
	if spec.Registry.Mode == v1alpha1.RegistryEmbedded || spec.Registry.Mode == "" {
		return ""
	}
	host := strings.TrimSpace(spec.Registry.SystemDefaultRegistry)
	if host == "" {
		host = t.RegistryHost
	}
	if host == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString("mirrors:\n")

	// A system default registry mirrors everything; explicit mirrors are listed
	// as the document wrote them.
	if len(spec.Registry.Mirrors) == 0 {
		b.WriteString("  \"*\":\n    endpoint:\n      - \"https://" + host + "\"\n")
	} else {
		for upstream, endpoints := range spec.Registry.Mirrors {
			b.WriteString("  \"" + upstream + "\":\n    endpoint:\n")
			for _, e := range endpoints {
				if !strings.Contains(e, "://") {
					e = "https://" + e
				}
				b.WriteString("      - \"" + e + "\"\n")
			}
		}
	}

	b.WriteString("configs:\n  \"" + host + "\":\n")
	if t.RegistryUser != "" || t.RegistryPass != "" {
		b.WriteString("    auth:\n")
		b.WriteString("      username: " + yamlString(t.RegistryUser) + "\n")
		b.WriteString("      password: " + yamlString(t.RegistryPass) + "\n")
	}
	insecure := spec.Registry.Insecure != nil && *spec.Registry.Insecure
	b.WriteString("    tls:\n")
	if insecure {
		b.WriteString("      insecure_skip_verify: true\n")
	} else if len(t.CABundle) > 0 {
		ca := t.RegistryCAPath
		if ca == "" {
			ca = caFileDebian
		}
		b.WriteString("      ca_file: " + ca + "\n")
	} else {
		b.WriteString("      insecure_skip_verify: false\n")
	}
	return b.String()
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
printf '%%s' %q | cmp -s - %s || { echo "%s differs from the document"; exit 1; }
echo "%s matches the document"`,
			registriesFile, registriesFile, body, registriesFile, registriesFile, registriesFile),
		Do: fmt.Sprintf(`set -e
install -d -m 0755 /etc/rancher/rke2
umask 077
printf '%%s' %q > %s
chmod 0600 %s`, body, registriesFile, registriesFile),
		Satisfied: "%s",
		Missing:   "%s",
	}
}

// yamlString quotes a value so a password containing a colon or a hash does not
// change the meaning of the document.
func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
