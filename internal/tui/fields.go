package tui

import (
	"strings"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/spec"
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
	case StepNodes:
		return []field{
			{labelKey: "nodes.server",
				get: func(c *Config) string { return c.Server },
				set: func(c *Config, v string) { c.Server = v }},
			{labelKey: "nodes.agents",
				get: func(c *Config) string { return strings.Join(c.Agents, ", ") },
				set: func(c *Config, v string) { c.Agents = splitList(v) }},
			{labelKey: "nodes.user",
				get: func(c *Config) string { return c.SSHUser },
				set: func(c *Config, v string) { c.SSHUser = v }},
			{labelKey: "nodes.port",
				get: func(c *Config) string { return c.SSHPort },
				set: func(c *Config, v string) { c.SSHPort = v }},
			// The password never reaches the document. cluster.yaml is handed
			// to customers and a plaintext secret in it is a liability, so this
			// value is passed straight to the preflight session and is not
			// written anywhere. Leave it empty to use an agent or a key.
			{labelKey: "nodes.password", secret: true, hint: "hint.password",
				get: func(c *Config) string { return c.SSHPassword },
				set: func(c *Config, v string) { c.SSHPassword = v }},
			{labelKey: "nodes.registration", hint: "hint.registration",
				get: func(c *Config) string { return c.Registration },
				set: func(c *Config, v string) { c.Registration = v }},
			{labelKey: "nodes.version",
				get: func(c *Config) string { return c.Version },
				set: func(c *Config, v string) { c.Version = v }},
			{labelKey: "nodes.domain",
				get: func(c *Config) string { return c.Domain },
				set: func(c *Config, v string) { c.Domain = v }},
		}

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
		return []field{
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

	case StepPKI:
		// Nothing is issued, so there is no account and no CA to describe.
		if w.cfg.PKIMode == string(v1alpha1.PKINone) || w.cfg.PKIMode == "" {
			return nil
		}
		if isACME(v1alpha1.PKIMode(w.cfg.PKIMode)) {
			return []field{
				{labelKey: "pki.email",
					get: func(c *Config) string { return c.ACMEEmail },
					set: func(c *Config, v string) { c.ACMEEmail = v }},
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

// networkMode is what the chosen profile fixes, since the wizard does not offer
// it as a separate question.
func (w *Wizard) networkMode() v1alpha1.NetworkMode {
	if b, ok := spec.BaselineFor(v1alpha1.ProfileName(w.cfg.Profile)); ok {
		return b.NetworkMode
	}
	return v1alpha1.NetworkOnline
}

func (w *Wizard) pkiMode() v1alpha1.PKIMode {
	if b, ok := spec.BaselineFor(v1alpha1.ProfileName(w.cfg.Profile)); ok {
		return b.PKIMode
	}
	return v1alpha1.PKIPrivateCA
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
