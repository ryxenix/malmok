package preflight

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ryxenix/malmok/api/v1alpha1"
	"github.com/ryxenix/malmok/internal/cert"
	"github.com/ryxenix/malmok/internal/codes"
	"github.com/ryxenix/malmok/internal/exec"
)

// Session runs preflight for a whole document.
//
// It resolves nothing itself. Reading a SourceRef needs the document's
// directory and its secret policy, both of which belong to the loader, so the
// caller hands in a Resolve function -- which also means the whole orchestrator
// is testable without a filesystem.

// Resolve reads what a SourceRef points at. secret marks fields where a
// literal:// value is refused unless the operator overrode it.
type Resolve func(field string, ref v1alpha1.SourceRef, secret bool) ([]byte, error)

// Dial opens a connection to one node. Replaced in tests.
type Dial func(ctx context.Context, cfg exec.SSHConfig) (exec.Runner, error)

// Session is one preflight run.
type Session struct {
	Spec v1alpha1.ClusterSpec
	// Dir is the document's directory, which relative paths resolve against.
	Dir string

	Resolve Resolve
	Dial    Dial
	Prober  *Prober

	// SSHPassword is used for nodes whose document entry names no credential.
	// It is the interactive path: an operator who typed a password into the
	// wizard has supplied one, and putting it into cluster.yaml instead would
	// write a plaintext secret into an artifact that gets handed to customers.
	SSHPassword string

	// SkipHostKeyCheck accepts any host key. Freshly installed nodes have no
	// known_hosts entry, and an operator who installed them is in a position to
	// say so -- but it is never the default.
	SkipHostKeyCheck bool

	// Emit is called for every result as it is produced, so a screen can draw
	// progress rather than waiting for the whole run.
	Emit func(ProbeResult)

	// Now is the clock the certificate windows are judged against. Zero means
	// the real one.
	Now time.Time
}

// Report is everything a preflight run found.
type Report struct {
	// Nodes is one entry per node that was reached.
	Nodes []NodeCapability
	// Document holds the results that are about the document or the set of
	// nodes rather than about one machine.
	Document []ProbeResult
	// Unreachable names the nodes that could not be connected to, with why.
	Unreachable map[string]string

	StartedAt time.Time
	Duration  time.Duration
}

// Blocking returns every finding that stops the run, node results included.
func (r Report) Blocking() []ProbeResult {
	var out []ProbeResult
	for _, p := range r.Document {
		if p.Failed() && p.Severity == codes.SeverityBlock {
			out = append(out, p)
		}
	}
	for _, n := range r.Nodes {
		out = append(out, n.Blocking()...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Counts totals the outcomes for a summary line.
func (r Report) Counts() (pass, fail, warn, skip int) {
	count := func(p ProbeResult) {
		switch {
		case p.Status == StatusSkip:
			skip++
		case p.Status == StatusFail && p.Severity == codes.SeverityBlock:
			fail++
		case p.Status == StatusFail:
			warn++
		default:
			pass++
		}
	}
	for _, p := range r.Document {
		count(p)
	}
	for _, n := range r.Nodes {
		for _, p := range n.Probes {
			count(p)
		}
	}
	return
}

// Run executes every probe the document calls for.
//
// Document-level checks run first and without touching a node: at a customer
// site the document is frequently written days before the machines exist, and a
// CIDR collision or a certificate covering the wrong name is worth finding then.
func (s *Session) Run(ctx context.Context) Report {
	started := time.Now()
	rep := Report{StartedAt: started.UTC(), Unreachable: map[string]string{}}

	rep.Document = s.documentChecks(ctx)
	for _, r := range rep.Document {
		s.emit(r)
	}

	nodes := s.nodes()
	if len(nodes) == 0 {
		p := s.Prober
		if p == nil {
			p = NewProber()
		}
		rep.Document = append(rep.Document, p.CheckVIPFree(ctx, s.Spec, nil))
		sort.Slice(rep.Document, func(i, j int) bool { return rep.Document[i].ID < rep.Document[j].ID })
		rep.Duration = time.Since(started)
		return rep
	}

	// Nodes are probed concurrently. A serial run over twenty nodes spends most
	// of its time waiting on round trips, and preflight is what an operator is
	// watching before they can start.
	type outcome struct {
		cap  NodeCapability
		zone string
		clk  Offset
		ok   bool
		err  error
		host string
	}
	results := make([]outcome, len(nodes))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, n := range nodes {
		wg.Add(1)
		go func(i int, n v1alpha1.NodeSpec) {
			defer wg.Done()
			out := outcome{host: n.Host}

			runner, err := s.connect(ctx, n)
			if err != nil {
				out.err = err
				results[i] = out
				return
			}
			defer runner.Close()

			node := &Node{Runner: runner, Spec: n, Cluster: s.Spec}
			out.cap = node.Probe(ctx)
			out.cap.Host = n.Host // the document's address, not the transport's
			out.zone = node.Timezone(ctx)
			delta, uncertainty, ok := node.Clock(ctx)
			out.clk, out.ok = Offset{Delta: delta, Uncertainty: uncertainty}, ok
			results[i] = out

			mu.Lock()
			for _, r := range out.cap.Probes {
				r.Node = n.Host
				s.emit(r)
			}
			mu.Unlock()
		}(i, n)
	}
	wg.Wait()

	clocks := map[string]Offset{}
	zones := map[string]string{}
	for _, o := range results {
		if o.err != nil {
			rep.Unreachable[o.host] = o.err.Error()
			continue
		}
		rep.Nodes = append(rep.Nodes, o.cap)
		zones[o.host] = o.zone
		if o.ok {
			clocks[o.host] = o.clk
		}
	}
	sort.Slice(rep.Nodes, func(i, j int) bool { return rep.Nodes[i].Host < rep.Nodes[j].Host })

	// The peer probes need the list of nodes that answered, which is only known
	// now. Running them earlier would measure against machines nobody reached.
	s.peerChecks(ctx, nodes, rep.Nodes)

	// PF-606 waits for the node results. Whether a VIP is free cannot be
	// answered from outside alone: an address that answers might be somebody
	// else's, or it might be the one kube-vip is serving for this very cluster,
	// and only the nodes can say which.
	held := map[string][]string{}
	for _, c := range rep.Nodes {
		held[c.Host] = c.Addresses
	}

	prober := s.Prober
	if prober == nil {
		prober = NewProber()
	}
	for _, r := range []ProbeResult{
		CheckHomogeneous(rep.Nodes),
		CheckHostnames(hostnamesOf(rep.Nodes)),
		s.clockSkew(ctx, nodes, clocks),
		CheckTimezones(zones),
		prober.CheckVIPFree(ctx, s.Spec, held),
	} {
		rep.Document = append(rep.Document, r)
		s.emit(r)
	}
	sort.Slice(rep.Document, func(i, j int) bool { return rep.Document[i].ID < rep.Document[j].ID })

	rep.Duration = time.Since(started)
	return rep
}

// peerChecks is a second pass because it needs every node to have answered.
func (s *Session) peerChecks(ctx context.Context, specs []v1alpha1.NodeSpec, caps []NodeCapability) {
	if len(caps) < 2 {
		return
	}
	// Two passes over the same connections. The port matrix is measured
	// against real listeners (see CheckPortMatrix), so every node has to be
	// listening before any node starts connecting -- interleaving the two
	// turns a race into a firewall finding.
	nodes := make([]*Node, len(caps))
	listening := make([]bool, len(caps))
	var wg sync.WaitGroup
	for i := range caps {
		spec, ok := findNodeSpec(specs, caps[i].Host)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(i int, spec v1alpha1.NodeSpec) {
			defer wg.Done()
			runner, err := s.connect(ctx, spec)
			if err != nil {
				return
			}
			nodes[i] = &Node{Runner: runner, Spec: spec, Cluster: s.Spec}
			listening[i] = nodes[i].StartPortListeners(ctx, caps[i].Host)
		}(i, spec)
	}
	wg.Wait()

	allListening := true
	for i := range caps {
		if nodes[i] != nil && !listening[i] {
			allListening = false
		}
	}

	for i := range caps {
		if nodes[i] == nil {
			continue
		}
		var peers []string
		for _, c := range caps {
			if c.Host != caps[i].Host {
				peers = append(peers, c.Host)
			}
		}
		wg.Add(1)
		go func(i int, peers []string) {
			defer wg.Done()
			node := nodes[i]
			node.ProbePeers(ctx, peers, &caps[i])
			if !allListening {
				// Without a listener on the far end a failed connect proves
				// nothing, and a passed one was luck. Say it was not measured
				// rather than guessing from RST behaviour -- the guess is the
				// bug this design replaced.
				caps[i].Probes["PF-601"] = unmeasured("PF-601",
					"a listener could not be arranged on every peer (systemd-socket-activate missing?), so reachability was not measured")
			}
			for _, id := range []string{"PF-601", "PF-602"} {
				if r, ok := caps[i].Probes[id]; ok {
					r.Node = caps[i].Host
					s.emit(r)
				}
			}
		}(i, peers)
	}
	wg.Wait()

	for i := range caps {
		if nodes[i] != nil {
			nodes[i].StopPortListeners(ctx)
			nodes[i].Runner.Close()
		}
	}
}

// clockSkew measures the spread, and measures it again when it looks wrong.
//
// The second look costs a pause and only in the failing case, and it is what
// separates "these nodes came up seconds apart and are still stepping" from
// "these clocks disagree". Preflight stays read-only either way: this reads
// `date` twice.
func (s *Session) clockSkew(ctx context.Context, nodes []v1alpha1.NodeSpec, first map[string]Offset) ProbeResult {
	got := CheckClockSkew(first, DefaultClockTolerance)
	if !got.Failed() || got.Code != "CLOCK_SKEW" {
		return got
	}

	select {
	case <-ctx.Done():
		return got
	case <-time.After(clockRecheckDelay):
	}

	second := map[string]Offset{}
	for _, n := range nodes {
		if _, ok := first[n.Host]; !ok {
			continue
		}
		runner, err := s.connect(ctx, n)
		if err != nil {
			return got
		}
		node := &Node{Runner: runner, Spec: n, Cluster: s.Spec}
		delta, uncertainty, ok := node.Clock(ctx)
		runner.Close()
		if !ok {
			return got
		}
		second[n.Host] = Offset{Delta: delta, Uncertainty: uncertainty}
	}
	return CheckClockSkewTrend(first, second, DefaultClockTolerance)
}

// clockRecheckDelay is long enough for a stepping time daemon to show its
// direction and short enough that nobody waits on it wondering.
const clockRecheckDelay = 20 * time.Second

// documentChecks are everything that needs no node.
func (s *Session) documentChecks(ctx context.Context) []ProbeResult {
	out := []ProbeResult{CheckCIDRs(s.Spec), CheckChartDir(s.Spec, s.Dir)}

	p := s.Prober
	if p == nil {
		p = NewProber()
	}
	out = append(out,
		p.CheckRegistrationAddress(ctx, s.Spec),
		p.CheckProxyConnect(ctx, s.Spec),
	)

	creds := s.registryCredentials()
	out = append(out, p.CheckRegistry(ctx, s.Spec, creds)...)

	token, _ := s.resolve("pki.acme.apiToken", acmeToken(s.Spec), true)
	out = append(out, p.CheckACME(ctx, s.Spec, len(token) > 0))

	out = append(out, CheckPKIMaterial(s.Spec, s.pkiMaterial(), s.Now)...)
	out = append(out, s.certChecks()...)

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// certChecks assembles every TLS bundle the gateways reference and turns the
// gates into probe results.
//
// One bundle per secret rather than per listener: a listener presents one
// certificate, and two listeners sharing a secret share the same one.
func (s *Session) certChecks() []ProbeResult {
	byRef := map[string]*v1alpha1.ListenerTLS{}
	for _, gw := range s.Spec.Gateway.Gateways {
		for i := range gw.Listeners {
			l := gw.Listeners[i]
			if l.TLS == nil || l.TLS.Source != v1alpha1.TLSFromBYO || l.TLS.SecretRef == "" {
				continue
			}
			byRef[l.TLS.SecretRef] = l.TLS
		}
	}
	if len(byRef) == 0 {
		why := "no listener supplies its own certificate"
		out := make([]ProbeResult, 0, 9)
		for _, id := range certGateIDs {
			out = append(out, skipped(id, why))
		}
		return out
	}

	refs := make([]string, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	var out []ProbeResult
	for _, ref := range refs {
		tls := byRef[ref]
		files, err := s.certFiles(ref, tls)
		if err != nil {
			out = append(out, fail("PF-903", "MATERIAL_UNREADABLE",
				fmt.Sprintf("the certificate material for %s could not be read: %v", ref, err)))
			continue
		}
		pass, _ := s.resolve("gateway."+ref+".tls.byo.passphrase", tls.BYO.Passphrase, true)

		res := cert.Assemble(cert.Input{
			Files:             files,
			Passphrase:        pass,
			Hostnames:         ListenerHostnames(s.Spec, ref),
			ExpiryWarningDays: tls.BYO.ExpiryWarningDays,
			IncludeRoot:       tls.BYO.IncludeRoot != nil && *tls.BYO.IncludeRoot,
			Now:               s.Now,
		})
		for _, r := range FromCert(res.Findings) {
			// The secret is named, because a document with two bundles produces
			// two PF-903s and the operator has to know which one failed.
			r.Detail = ref + ": " + r.Detail
			out = append(out, r)
		}
	}
	return out
}

// certGateIDs are the gates internal/cert produces.
var certGateIDs = []string{
	"PF-901", "PF-902", "PF-903", "PF-904", "PF-905", "PF-906", "PF-910", "PF-911", "PF-912",
}

// certFiles gathers the material for one bundle, from a directory or from the
// explicit cert/key/chain fields.
func (s *Session) certFiles(ref string, tls *v1alpha1.ListenerTLS) ([]cert.File, error) {
	if tls.BYO == nil {
		return nil, fmt.Errorf("tls.source is byo and tls.byo is absent")
	}
	field := "gateway." + ref + ".tls.byo"

	if tls.BYO.Dir != "" {
		dir := resolveLocalPath(string(tls.BYO.Dir), s.Dir)
		return cert.LoadDir(dir)
	}

	var files []cert.File
	for _, f := range []struct {
		name   string
		ref    v1alpha1.SourceRef
		secret bool
	}{
		{"cert", tls.BYO.Cert, false},
		{"key", tls.BYO.Key, true},
		{"chain", tls.BYO.Chain, false},
	} {
		if f.ref == "" {
			continue
		}
		body, err := s.resolve(field+"."+f.name, f.ref, f.secret)
		if err != nil {
			return nil, err
		}
		files = append(files, cert.File{Name: field + "." + f.name, Data: body})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("neither dir nor cert/key is set")
	}
	return files, nil
}

// registryCredentials resolves what PF-701..703 need.
func (s *Session) registryCredentials() RegistryCredentials {
	user, _ := s.resolve("registry.username", s.Spec.Registry.Username, true)
	pass, _ := s.resolve("registry.password", s.Spec.Registry.Password, true)
	ca, _ := s.resolve("registry.caCert", s.Spec.Registry.CACert, false)
	return RegistryCredentials{Username: string(user), Password: string(pass), CACert: ca}
}

// pkiMaterial resolves what PF-704..706 need.
func (s *Session) pkiMaterial() PKIMaterial {
	var m PKIMaterial
	if ca := s.Spec.PKI.PrivateCA; ca != nil {
		m.RootCert, _ = s.resolve("pki.privateCA.rootCert", ca.RootCert, false)
		m.IntermediateCert, _ = s.resolve("pki.privateCA.intermediateCert", ca.IntermediateCert, false)
		m.IntermediateKey, _ = s.resolve("pki.privateCA.intermediateKey", ca.IntermediateKey, true)
	}
	m.RegistryCA, _ = s.resolve("registry.caCert", s.Spec.Registry.CACert, false)
	return m
}

// connect opens a session to one node with the document's credentials.
func (s *Session) connect(ctx context.Context, n v1alpha1.NodeSpec) (exec.Runner, error) {
	cfg := exec.FromSpec(n)
	cfg.InsecureSkipHostKeyCheck = s.SkipHostKeyCheck

	field := "topology." + n.Host + ".ssh"
	key, err := s.resolve(field+".privateKey", n.SSH.PrivateKey, true)
	if err != nil {
		return nil, err
	}
	cfg.PrivateKey = key

	pw, err := s.resolve(field+".password", n.SSH.Password, true)
	if err != nil {
		return nil, err
	}
	if len(pw) == 0 && len(key) == 0 {
		pw = []byte(s.SSHPassword)
	}
	cfg.Password = string(pw)

	dial := s.Dial
	if dial == nil {
		dial = exec.Connect
	}
	runner, err := dial(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Preflight reads /proc/config.gz, the lockdown state and the packet filter
	// ruleset, none of which an unprivileged account can see. Without elevation
	// those probes report "could not be measured", which is honest and useless.
	if n.SSH.Become == nil || *n.SSH.Become {
		become, err := s.resolve(field+".becomePassword", n.SSH.BecomePassword, true)
		if err != nil {
			runner.Close()
			return nil, err
		}
		if len(become) == 0 {
			become = pw
		}
		// Proved rather than assumed. `sudo -n` on an account that needs a
		// password exits non-zero with the command never having run, and a
		// non-zero exit is how every probe spells "no": the node would report
		// no BTF, no bpf filesystem and no module support, all of which would
		// be measurements of the account.
		elevated, err := exec.Elevate(ctx, runner, string(become))
		if err != nil {
			runner.Close()
			return nil, err
		}
		return elevated, nil
	}
	return runner, nil
}

func (s *Session) resolve(field string, ref v1alpha1.SourceRef, secret bool) ([]byte, error) {
	if ref == "" || s.Resolve == nil {
		return nil, nil
	}
	return s.Resolve(field, ref, secret)
}

func (s *Session) emit(r ProbeResult) {
	if s.Emit != nil {
		s.Emit(r)
	}
}

// nodes is every node in the document, servers first.
func (s *Session) nodes() []v1alpha1.NodeSpec {
	out := make([]v1alpha1.NodeSpec, 0, len(s.Spec.Topology.Servers)+len(s.Spec.Topology.Agents))
	for _, n := range s.Spec.Topology.Servers {
		if n.Role == "" {
			n.Role = v1alpha1.RoleServer
		}
		out = append(out, n)
	}
	for _, n := range s.Spec.Topology.Agents {
		if n.Role == "" {
			n.Role = v1alpha1.RoleAgent
		}
		out = append(out, n)
	}
	return out
}

func findNodeSpec(specs []v1alpha1.NodeSpec, host string) (v1alpha1.NodeSpec, bool) {
	for _, n := range specs {
		if n.Host == host {
			return n, true
		}
	}
	return v1alpha1.NodeSpec{}, false
}

func hostnamesOf(caps []NodeCapability) map[string]string {
	out := map[string]string{}
	for _, c := range caps {
		out[c.Host] = c.Hostname
	}
	return out
}

func acmeToken(spec v1alpha1.ClusterSpec) v1alpha1.SourceRef {
	if spec.PKI.ACME == nil {
		return ""
	}
	return spec.PKI.ACME.APIToken
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// Mark is the one-word status a report line carries.
func Mark(p ProbeResult) string {
	switch {
	case p.Status == StatusSkip:
		return "SKIP"
	case p.Status == StatusFail && p.Severity == codes.SeverityBlock:
		return "FAIL"
	case p.Status == StatusFail:
		return "WARN"
	}
	return "OK"
}

// SortedProbes returns one node's results in code order.
func SortedProbes(c NodeCapability) []ProbeResult {
	out := make([]ProbeResult, 0, len(c.Probes))
	for _, p := range c.Probes {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Summary is the one line that goes at the end of a run.
func (r Report) Summary() string {
	pass, fail, warn, skip := r.Counts()
	s := fmt.Sprintf("%d checks in %s: %d pass, %d fail, %d warn, %d skip",
		pass+fail+warn+skip, r.Duration.Round(time.Millisecond), pass, fail, warn, skip)
	if len(r.Unreachable) > 0 {
		// With the reason: "could not reach 10.0.0.11" sends an operator to
		// the network when the answer was a host key, a password or a
		// missing account, and the reason was in hand all along.
		hosts := make([]string, 0, len(r.Unreachable))
		for h := range r.Unreachable {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		var why []string
		for _, h := range hosts {
			why = append(why, h+" ("+r.Unreachable[h]+")")
		}
		s += "; could not reach " + strings.Join(why, ", ")
	}
	return s
}
