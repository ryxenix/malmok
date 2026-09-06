package tui

import (
	"strings"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/exec"
)

// A field is one editable line on a screen.
//
// Editing used to be hardcoded to the nodes screen. Generalising it is what
// lets a screen exist for every group of values the validator asks about,
// rather than only for the group that happened to be written first.
type field struct {
	labelKey string
	get      func(*Config) string
	set      func(*Config, string)

	// Secret masks the value on screen. The value itself is a SourceRef, so
	// what is hidden is a reference rather than a password -- but a token typed
	// into a terminal at a customer site is still read over somebody's
	// shoulder.
	secret bool

	// Hint is a catalogue key shown under the field when it is focused.
	hint string
}

// fieldsFor returns the editable lines of a step, or nil for a step that has
// none.
//
// PKI adapts to the mode the profile chose: asking for an offline CA's
// intermediate key on a cluster that uses ACME would be asking for something
// that does not exist.
func (w *Wizard) fieldsFor(step Step) []field {
	switch step {
	case StepOpen:
		return []field{
			{labelKey: "open.path",
				get:  func(c *Config) string { return c.DocPath },
				set:  func(c *Config, v string) { c.DocPath = v },
				hint: "open.path.hint"},
		}
	case StepTarget:
		return []field{
			{labelKey: "target.version",
				get:  func(c *Config) string { return c.UpgradeTo },
				set:  func(c *Config, v string) { c.UpgradeTo = v },
				hint: "target.version.hint"},
		}
	case StepNodes:
		// The version first, under the channel choices at the top of the
		// screen: it decides what every node runs, and it sat below the SSH
		// credentials where the most consequential answer on the screen was
		// the last one an operator reached.
		fs := []field{
			{labelKey: "nodes.version", hint: "hint.version",
				get: func(c *Config) string { return c.Version },
				set: func(c *Config, v string) { c.Version = v }},
		}
		// Not asked for when the answer is this machine: the address is chosen
		// from the ones the machine reports, above the fields.
		if !w.cfg.Local {
			fs = append(fs, field{labelKey: "nodes.server",
				get: func(c *Config) string { return c.Server },
				set: func(c *Config, v string) { c.Server = v }})
		}
		fs = append(fs, []field{
			{labelKey: "nodes.agents",
				get: func(c *Config) string { return strings.Join(c.Agents, ", ") },
				set: func(c *Config, v string) { c.Agents = splitList(v) }},
		}...)

		// The credentials appear only when something is dialled. A node whose
		// address belongs to this machine is reached without a connection, and
		// an SSH user and port on that screen are two questions with no answer
		// -- worse, they read as though the tool were about to log in somewhere.
		if w.needsSSH() {
			fs = append(fs,
				field{labelKey: "nodes.user",
					get: func(c *Config) string { return c.SSHUser },
					set: func(c *Config, v string) { c.SSHUser = v }},
				field{labelKey: "nodes.port",
					get: func(c *Config) string { return c.SSHPort },
					set: func(c *Config, v string) { c.SSHPort = v }},
			)
		}

		// The password stays either way, and means a different thing in each.
		// Over SSH it logs in and then elevates; locally it only elevates, and
		// it is still needed, because an account that cannot elevate answers
		// "no" to every privileged question. Empty is right when a key or an
		// agent is used, or when the tool is already running as root.
		//
		// It never reaches the document. cluster.yaml is handed to customers
		// and a plaintext secret in it is a liability, so this value goes
		// straight to the session and is written nowhere.
		password := field{labelKey: "nodes.password", secret: true, hint: "hint.password",
			get: func(c *Config) string { return c.SSHPassword },
			set: func(c *Config, v string) { c.SSHPassword = v }}
		if !w.needsSSH() {
			password.labelKey, password.hint = "nodes.sudopassword", "hint.sudopassword"
		}
		fs = append(fs, password)

		fs = append(fs,
			field{labelKey: "nodes.registration", hint: "hint.registration",
				get: func(c *Config) string { return c.Registration },
				set: func(c *Config, v string) { c.Registration = v }},
			field{labelKey: "nodes.domain",
				get: func(c *Config) string { return c.Domain },
				set: func(c *Config, v string) { c.Domain = v }},
		)

		// Where RKE2's own artifacts sit, asked beside the version because it
		// is the version they have to be for. Only in an air gap: everywhere
		// else the installer fetches them, and a field for a path nobody
		// staged is a question with no answer.
		if w.networkMode() == v1alpha1.NetworkAirgap {
			fs = append(fs, field{labelKey: "nodes.artifacts", hint: "hint.artifacts",
				get: func(c *Config) string { return c.ArtifactPath },
				set: func(c *Config, v string) { c.ArtifactPath = v }})
		}
		return fs

	case StepGateway:
		if w.cfg.Exposure == "none" {
			return nil
		}
		fs := []field{
			{labelKey: "gw.name",
				get: func(c *Config) string { return c.GatewayName },
				set: func(c *Config, v string) { c.GatewayName = v }},
		}
		if w.cfg.Exposure == "lb-pool" {
			// The pool it takes an address from, and the address itself: a
			// site that needs the DNS record before the install needs to say
			// which address it will be (PF-612) rather than discover it after.
			fs = append(fs,
				field{labelKey: "net.lbpool", hint: "hint.lbpool",
					get: func(c *Config) string { return strings.Join(c.LBPool, ", ") },
					set: func(c *Config, v string) { c.LBPool = splitList(v) }},
				field{labelKey: "gw.address", hint: "hint.gwaddress",
					get: func(c *Config) string { return c.GatewayAddress },
					set: func(c *Config, v string) { c.GatewayAddress = v }},
			)
		}
		return fs

	case StepNetwork:
		f := []field{
			{labelKey: "net.lbpool", hint: "hint.lbpool",
				get: func(c *Config) string { return strings.Join(c.LBPool, ", ") },
				set: func(c *Config, v string) { c.LBPool = splitList(v) }},
		}
		// Only where there is a proxy to describe. A DMZ profile needs these
		// and a homelab has nothing to put in them.
		if w.networkMode() == v1alpha1.NetworkProxy {
			f = append([]field{
				{labelKey: "net.proxyHTTP",
					get: func(c *Config) string { return c.ProxyHTTP },
					set: func(c *Config, v string) { c.ProxyHTTP = v }},
				{labelKey: "net.proxyHTTPS",
					get: func(c *Config) string { return c.ProxyHTTPS },
					set: func(c *Config, v string) { c.ProxyHTTPS = v }},
				{labelKey: "net.noProxy", hint: "hint.noproxy",
					get: func(c *Config) string { return strings.Join(c.NoProxy, ", ") },
					set: func(c *Config, v string) { c.NoProxy = splitList(v) }},
			}, f...)
		}
		return f

	case StepOptions:
		// Only what the chosen driver actually needs. A screen that asks for an
		// NFS export on a Longhorn cluster is asking for something that will
		// never be used.
		if w.cfg.Storage == string(v1alpha1.StorageNFS) {
			return []field{
				{labelKey: "st.server",
					get: func(c *Config) string { return c.NFSServer },
					set: func(c *Config, v string) { c.NFSServer = v }},
				{labelKey: "st.path",
					get: func(c *Config) string { return c.NFSPath },
					set: func(c *Config, v string) { c.NFSPath = v }},
			}
		}
		return nil

	case StepRegistry:
		// Only the modes that point somewhere need an address. The embedded
		// mirror and a plain upstream pull have nothing to configure.
		if w.cfg.RegistryMode != string(v1alpha1.RegistryExternal) &&
			w.cfg.RegistryMode != string(v1alpha1.RegistryBYO) {
			return nil
		}
		fs := []field{
			{labelKey: "reg.host", hint: "hint.registry",
				get: func(c *Config) string { return c.RegistryHost },
				set: func(c *Config, v string) { c.RegistryHost = v }},
			{labelKey: "reg.user",
				get: func(c *Config) string { return c.RegistryUser },
				set: func(c *Config, v string) { c.RegistryUser = v }},
			{labelKey: "reg.pass", secret: true, hint: "hint.sourceref",
				get: func(c *Config) string { return c.RegistryPass },
				set: func(c *Config, v string) { c.RegistryPass = v }},
			{labelKey: "reg.ca",
				get: func(c *Config) string { return c.RegistryCA },
				set: func(c *Config, v string) { c.RegistryCA = v }},
		}
		// Where the charts are mirrored. Asked beside the registry because a
		// site that mirrors images mirrors charts in the same place, and
		// required in an air gap for the same reason the bundle is: without
		// it the run reaches l2-pki and stops, twenty minutes past the point
		// where the screen could have asked.
		fs = append(fs, field{labelKey: "reg.charts", hint: "hint.charts",
			get: func(c *Config) string { return c.RegistryChartRepo },
			set: func(c *Config, v string) { c.RegistryChartRepo = v }})

		return fs

	case StepPKI:
		// Nothing is issued, so there is no account and no CA to describe.
		if w.cfg.PKIMode == string(v1alpha1.PKINone) || w.cfg.PKIMode == "" {
			return nil
		}
		// The certificate is supplied rather than issued, so what it needs is
		// the material -- not a CA to sign with. This fell through to the
		// private-CA fields, which asked for an issuing CA's key on a build
		// that issues nothing and produced a document the validator refuses
		// with no screen able to fix it.
		if v1alpha1.PKIMode(w.cfg.PKIMode) == v1alpha1.PKIBYOCert {
			return []field{
				{labelKey: "pki.byocert", hint: "hint.sourceref",
					get: func(c *Config) string { return c.BYOCert },
					set: func(c *Config, v string) { c.BYOCert = v }},
				{labelKey: "pki.byokey", secret: true, hint: "hint.sourceref",
					get: func(c *Config) string { return c.BYOKey },
					set: func(c *Config, v string) { c.BYOKey = v }},
				{labelKey: "pki.byoca",
					get: func(c *Config) string { return c.BYOCA },
					set: func(c *Config, v string) { c.BYOCA = v }},
			}
		}
		if isACME(v1alpha1.PKIMode(w.cfg.PKIMode)) {
			return []field{
				{labelKey: "pki.email",
					get: func(c *Config) string { return c.ACMEEmail },
					set: func(c *Config, v string) { c.ACMEEmail = v }},
				// Staging or production. Getting this wrong on a real domain
				// spends a rate limit that resets in a week.
				{labelKey: "pki.server", hint: "hint.acmeserver",
					get: func(c *Config) string { return c.ACMEServer },
					set: func(c *Config, v string) { c.ACMEServer = v }},
				{labelKey: "pki.provider",
					get: func(c *Config) string { return c.ACMEProvider },
					set: func(c *Config, v string) { c.ACMEProvider = v }},
				{labelKey: "pki.token", secret: true, hint: "hint.sourceref",
					get: func(c *Config) string { return c.ACMEToken },
					set: func(c *Config, v string) { c.ACMEToken = v }},
			}
		}
		return []field{
			{labelKey: "pki.root", hint: "hint.sourceref",
				get: func(c *Config) string { return c.CARoot },
				set: func(c *Config, v string) { c.CARoot = v }},
			{labelKey: "pki.inter",
				get: func(c *Config) string { return c.CAIntermediate },
				set: func(c *Config, v string) { c.CAIntermediate = v }},
			{labelKey: "pki.key", secret: true, hint: "hint.rootkey",
				get: func(c *Config) string { return c.CAKey },
				set: func(c *Config, v string) { c.CAKey = v }},
		}
	}
	return nil
}

// networkMode is what the operator chose, falling back to what the profile
// suggested and then to online.
//
// Read from the configuration rather than from the baseline: the profile fills
// this in when it is chosen, and reading the baseline back would mean the
// screen ignored every change made after that.
func (w *Wizard) networkMode() v1alpha1.NetworkMode {
	if m := v1alpha1.NetworkMode(w.cfg.NetworkMode); m != "" {
		return m
	}
	return v1alpha1.NetworkOnline
}

func isACME(m v1alpha1.PKIMode) bool {
	return m == v1alpha1.PKIACMEDNS01 || m == v1alpha1.PKIACMEHTTP01
}

func isCA(mode string) bool { return v1alpha1.PKIMode(mode) == v1alpha1.PKIPrivateCA }

// values reads the current contents of a step's fields.
func (w *Wizard) values(step Step) []string {
	fs := w.fieldsFor(step)
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.get(&w.cfg)
	}
	return out
}

// labels reads a step's field labels, translated.
func (w *Wizard) labels(step Step) []string {
	fs := w.fieldsFor(step)
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = w.cat.T(f.labelKey)
	}
	return out
}

// masked reports which of a step's fields are hidden on screen.
func (w *Wizard) masked(step Step) []bool {
	fs := w.fieldsFor(step)
	out := make([]bool, len(fs))
	for i, f := range fs {
		out[i] = f.secret
	}
	return out
}

// setValue writes one field.
func (w *Wizard) setValue(step Step, i int, v string) {
	fs := w.fieldsFor(step)
	if i >= 0 && i < len(fs) {
		fs[i].set(&w.cfg, v)
	}
}

// fieldHint is the line shown under a focused field, if it has one.
func (w *Wizard) fieldHint(step, i int) string {
	fs := w.fieldsFor(Step(step))
	if i < 0 || i >= len(fs) || fs[i].hint == "" {
		return ""
	}
	return w.cat.T(fs[i].hint)
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// needsSSH reports whether any node in the configuration has to be dialled.
//
// One node that is not this machine is enough: the credentials are asked for
// once and used for every connection, so hiding them because the server happens
// to be local would leave the agents unreachable.
func (w *Wizard) needsSSH() bool {
	for _, host := range w.allHosts() {
		if !exec.IsLocal(host) {
			return true
		}
	}
	return false
}

// allHosts is every address the configuration names, in screen order.
func (w *Wizard) allHosts() []string {
	out := make([]string, 0, 1+len(w.cfg.Agents))
	if h := strings.TrimSpace(w.cfg.Server); h != "" {
		out = append(out, h)
	}
	for _, a := range w.cfg.Agents {
		if h := strings.TrimSpace(a); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// nodesHelp is what the node screen explains, which depends on whether
// anything is dialled.
//
// The screen looks different in the two cases -- there is no SSH user and no
// port when nothing is connected to -- and an explanation that talked about
// entering nodes to install on either way would leave the operator to work out
// why two fields had gone.
func (w *Wizard) nodesHelp() string {
	if w.needsSSH() {
		return "nodes.help"
	}
	return "nodes.help.local"
}

// setLocal switches the wizard between building here and building elsewhere.
//
// Choosing this machine fills the server address in from what the machine
// already knows. Choosing another machine clears it, because the address that
// was right for here is wrong for anywhere else and leaving it would be the
// wizard suggesting a node that does not exist.
func (w *Wizard) setLocal(local bool) {
	if w.cfg.Local == local {
		return
	}
	w.cfg.Local = local
	w.cursor[StepNodes] = 0

	if !local {
		if exec.IsLocal(w.cfg.Server) {
			w.cfg.Server = ""
		}
		return
	}
	if addrs := exec.LocalIPv4s(); len(addrs) > 0 {
		w.cfg.Server = addrs[0]
	}
}

// localAddressCount is how many rows the address chooser takes on the node
// screen, which is none unless this machine is the one being built on.
func (w *Wizard) localAddressCount() int {
	if !w.cfg.Local {
		return 0
	}
	return len(exec.LocalIPv4s())
}

// registryExtraRows is how many choice rows sit above the registry fields.
//
// Whether to accept a certificate the registry cannot prove is a decision, not
// a field, and it only arises for a registry this document points at: the
// embedded mirror and a plain upstream pull have no such question.
// gatewayExtraRows is how many choice rows sit below the exposure choice.
//
// Only when something is exposed. HTTP/2 is a property of a TLS listener, and
// a gateway that serves nothing has none -- asking there is a question with no
// consequence, which is how a wizard teaches people to stop reading it.
func (w *Wizard) gatewayExtraRows() int {
	if w.cfg.Exposure == "" || w.cfg.Exposure == "none" {
		return 0
	}
	return 2
}

func (w *Wizard) registryExtraRows() int {
	if w.registryHasAddress() {
		return 2
	}
	return 0
}

func (w *Wizard) registryHasAddress() bool {
	return w.cfg.RegistryMode == string(v1alpha1.RegistryExternal) ||
		w.cfg.RegistryMode == string(v1alpha1.RegistryBYO)
}

// pkiExtraRows is how many choice rows sit above the certificate fields.
//
// Only for a private CA. Distributing a public CA's root to every namespace is
// noise, and it is the one mode where l2-pki reads the answer -- a cluster
// built without it has a CA the nodes trust and the pods do not.
func (w *Wizard) pkiExtraRows() int {
	if v1alpha1.PKIMode(w.cfg.PKIMode) == v1alpha1.PKIPrivateCA {
		return 2
	}
	return 0
}
