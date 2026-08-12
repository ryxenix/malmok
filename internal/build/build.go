// Package build drives a wizard-collected configuration through the same
// pipeline `apply -f` runs.
//
// It lives outside cmd/ so it can be exercised against real nodes without a
// terminal. The wizard is a way of producing a document rather than a second
// installer, and that claim is only worth making if it can be checked.
package build

import (
	"context"
	"fmt"
	"strings"
	"time"

	"platform.ryxen.dev/platformctl/api/v1alpha1"
	"platform.ryxen.dev/platformctl/internal/catalogue"
	"platform.ryxen.dev/platformctl/internal/dataplane"
	"platform.ryxen.dev/platformctl/internal/engine"
	"platform.ryxen.dev/platformctl/internal/event"
	"platform.ryxen.dev/platformctl/internal/exec"
	"platform.ryxen.dev/platformctl/internal/gateway"
	"platform.ryxen.dev/platformctl/internal/plan"
	"platform.ryxen.dev/platformctl/internal/preflight"
	"platform.ryxen.dev/platformctl/internal/report"
	"platform.ryxen.dev/platformctl/internal/rke2"
	"platform.ryxen.dev/platformctl/internal/state"
	"platform.ryxen.dev/platformctl/internal/upgrade"
)

// Session is the connections and findings one wizard-driven build shares
// between its two steps.
//
// The checks and the install are separate screens and separate Work functions,
// but they are one visit to the same nodes: opening a second set of connections
// would double the handshakes and, worse, let the checks pass on a connection
// the install then fails to make.
type Session struct {
	// InsecureHostKey accepts any SSH host key. Freshly installed nodes have
	// no known_hosts entry, and an operator who installed them can say so.
	InsecureHostKey bool

	runners catalogue.Runners
	closers []func() error

	// caps is what preflight found, which the plan is a function of.
	caps []preflight.NodeCapability
}

// Close releases every connection the session opened.
func (s *Session) Close() {
	for _, c := range s.closers {
		_ = c()
	}
	s.runners = catalogue.Runners{}
	s.closers = nil
}

// connect opens a shell on every node the wizard's configuration names.
//
// Credentials come from the wizard rather than from the document: cluster.yaml
// is an audit artifact handed to customers, and a plaintext password in one is
// what the schema's SourceRef indirection exists to prevent.
// Connect opens a shell on every node the configuration names.
func (s *Session) Connect(ctx context.Context, spec v1alpha1.ClusterSpec, password string) error {
	if s.runners.ByHost != nil {
		return nil
	}
	nodes := append(append([]v1alpha1.NodeSpec{}, spec.Topology.Servers...), spec.Topology.Agents...)
	if len(nodes) == 0 {
		return fmt.Errorf("no node was entered")
	}

	byHost := map[string]exec.Runner{}
	for _, n := range nodes {
		cfg := exec.FromSpec(n)
		cfg.Password = password
		cfg.InsecureSkipHostKeyCheck = s.InsecureHostKey

		runner, err := exec.Connect(ctx, cfg)
		if err != nil {
			s.Close()
			return fmt.Errorf("%s could not be reached: %w", n.Host, err)
		}
		s.closers = append(s.closers, runner.Close)

		// Every phase reads files an unprivileged account cannot and writes
		// files only root may write -- and the elevation is proved here rather
		// than assumed, because an account that cannot elevate answers "no" to
		// every privileged question and the node reads as one that cannot run
		// the dataplane.
		elevated, err := exec.Elevate(ctx, runner, password)
		if err != nil {
			s.Close()
			return err
		}
		byHost[n.Host] = elevated
	}

	s.runners.ByHost = byHost
	s.runners.Control = byHost[spec.Topology.Servers[0].Host]
	return nil
}

// Preflight measures every node and reports through the event stream.
//
// The findings are kept because the plan is a function of them and the install
// step runs next: measuring twice would let the two disagree about the same
// cluster.
func (s *Session) Preflight(ctx context.Context, spec v1alpha1.ClusterSpec,
	password string, w *event.Writer) error {

	if err := s.Connect(ctx, spec, password); err != nil {
		return err
	}

	emitter := preflight.Emitter{Writer: w}
	session := &preflight.Session{
		Spec:             spec,
		SSHPassword:      password,
		SkipHostKeyCheck: s.InsecureHostKey,
		Emit:             emitter.Emit,
		Dial: func(_ context.Context, cfg exec.SSHConfig) (exec.Runner, error) {
			if r, ok := s.runners.ByHost[cfg.Host]; ok {
				return noCloseRunner{r}, nil
			}
			return nil, fmt.Errorf("no connection was opened to %s", cfg.Host)
		},
		// The wizard builds its document in memory, so there is nothing on disk
		// for a SourceRef to point at.
		Resolve: func(string, v1alpha1.SourceRef, bool) ([]byte, error) { return nil, nil },
	}

	rep := session.Run(ctx)
	s.caps = rep.Nodes

	for host, why := range rep.Unreachable {
		_, _ = w.Emit(event.Event{
			Kind: event.KindProbe, Phase: preflight.PhasePreflight,
			Step: "connect", Node: host, Status: event.StatusBlocked,
			Code: "PF-603", Detail: "could not be reached: " + why,
		})
	}

	if n := len(rep.Blocking()) + len(rep.Unreachable); n > 0 {
		return fmt.Errorf("%d checks block the install", n)
	}
	return nil
}

// Install builds the cluster the wizard collected.
//
// It is the same pipeline `apply -f` runs, on the same engine, writing the same
// events. The wizard is a way of producing the document, not a second installer
// (ADR-002).
func (s *Session) Install(ctx context.Context, spec v1alpha1.ClusterSpec,
	password string, w *event.Writer, runDir string, st *state.State, recheck bool) error {

	if err := s.Connect(ctx, spec, password); err != nil {
		return err
	}
	if len(s.caps) == 0 {
		return fmt.Errorf("the checks have not run, so there is nothing to plan against")
	}

	p, err := plan.Generate(spec, s.caps, plan.Options{})
	if err != nil {
		return err
	}
	if err := report.SavePlan(runDir, p); err != nil {
		return err
	}

	// A downgrade is a decision, and the document says who gets to make it.
	// Deciding silently here would be the tool choosing on the operator's
	// behalf at the one moment they are watching.
	for _, d := range p.Downgrades {
		_, _ = w.Emit(event.Event{
			Kind: event.KindDecision, Code: d.Code,
			Detail: fmt.Sprintf("%s -> %s, triggered by %s on %s",
				d.From, d.To, strings.Join(d.TriggeredBy, ", "), strings.Join(d.Nodes, ", ")),
		})
	}
	if err := downgradeAllowed(spec, p); err != nil {
		return err
	}

	phases, err := catalogue.Build(spec, s.runners, catalogue.Material{}, catalogue.Options{
		RKE2:      rke2.Options{InstallTimeout: 20 * time.Minute, ReadyTimeout: 15 * time.Minute},
		Dataplane: dataplane.Options{Timeout: 15 * time.Minute},
		Gateway:   gateway.Options{Timeout: 10 * time.Minute},
	})
	if err != nil {
		return err
	}

	runner := &engine.Runner{
		Events:    w,
		State:     st,
		StatePath: state.Path(runDir),
		Resume:    state.Options{Recheck: recheck},
	}
	if err := runner.Run(ctx, phases); err != nil {
		return err
	}

	// The artifacts are the point of having a run directory. Produced here
	// rather than by the operator afterwards, because the one time somebody is
	// certain to have them is when the build just finished.
	if _, err := report.Write(mustLoadRun(runDir)); err != nil {
		return err
	}
	return nil
}

// downgradeAllowed applies the document's own policy.
//
// `confirm` is the default for airgapped profiles precisely because an
// unnoticed downgrade discovered months later at a customer site is expensive
// to explain, and the wizard has no way to ask -- so it stops and says what it
// would have done.
func downgradeAllowed(spec v1alpha1.ClusterSpec, p *plan.Plan) error {
	if !p.Downgraded() {
		return nil
	}
	var what []string
	for _, d := range p.Downgrades {
		what = append(what, fmt.Sprintf("%s (%s -> %s, %s)",
			d.Code, d.From, d.To, strings.Join(d.TriggeredBy, ", ")))
	}
	joined := strings.Join(what, "; ")

	switch spec.Kubernetes.Dataplane.DowngradePolicy {
	case v1alpha1.DowngradeAuto:
		return nil
	case v1alpha1.DowngradeForbid:
		return fmt.Errorf("the nodes cannot run what the document asked for and "+
			"downgradePolicy is forbid: %s", joined)
	default:
		// confirm, and anything unset on a profile that did not choose.
		return fmt.Errorf("the nodes cannot run what the document asked for: %s. "+
			"downgradePolicy is confirm, so nothing has been installed -- write the "+
			"configuration out and run `platformctl apply -f cluster.yaml --approve` "+
			"to accept it", joined)
	}
}

// mustLoadRun reads a run back for the report. A run directory this process
// just wrote and cannot read is a bug worth surfacing rather than hiding.
func mustLoadRun(dir string) *report.Run {
	r, err := report.Load(dir)
	if err != nil {
		return &report.Run{Dir: dir}
	}
	return r
}

// noCloseRunner lends a connection without giving up ownership of it.
//
// Preflight is handed the connections the session already holds; one that
// closed them would leave the install phases with nothing to talk to.
type noCloseRunner struct{ exec.Runner }

func (noCloseRunner) Close() error { return nil }

// Upgrade moves the cluster the document names to a new version.
//
// The same shape as Install and for the same reason: the wizard is a way of
// choosing what to do, not a second implementation of doing it. Measure what
// the nodes are running, decide whether the step is legal from there, and only
// then touch anything.
func (s *Session) Upgrade(ctx context.Context, spec v1alpha1.ClusterSpec, target, password string,
	w *event.Writer, runDir string, st *state.State) error {

	if err := s.Connect(ctx, spec, password); err != nil {
		return err
	}

	current := upgrade.Read(ctx, spec, s.runners.ByHost, s.runners.Control)
	results := upgrade.Check(current, target)

	// The preconditions reach the screen as events, never as a return value:
	// ADR-002 keeps the renderer a consumer of the stream, so a refused upgrade
	// looks the same whether or not anybody was watching it.
	emitter := preflight.Emitter{Writer: w}
	for _, r := range results {
		emitter.Emit(r)
	}
	if blocking := upgrade.Blocking(results); len(blocking) > 0 {
		return fmt.Errorf("%d precondition(s) stop the upgrade: %s",
			len(blocking), blocking[0].Detail)
	}

	phases, err := upgrade.Phases(spec, target, upgrade.Runners{
		ByHost: s.runners.ByHost, Control: s.runners.Control,
	}, upgrade.Options{
		RKE2:         rke2.Options{InstallTimeout: 20 * time.Minute, ReadyTimeout: 15 * time.Minute},
		DrainTimeout: 10 * time.Minute,
		// One node has nowhere to drain to, and evicting workloads that have no
		// other home would wait for pods that can never be scheduled.
		SingleNode: len(current.Nodes) < 2,
	})
	if err != nil {
		return err
	}

	runner := &engine.Runner{
		Events:    w,
		State:     st,
		StatePath: state.Path(runDir),
	}
	return runner.Run(ctx, phases)
}
