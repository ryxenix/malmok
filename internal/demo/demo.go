// Package demo provides a phase set backed by simulated steps.
//
// SCAFFOLDING, NOT A FEATURE
//
//	No node is contacted and nothing is installed. It exists so the runner, the
//	event stream and a renderer can be exercised end to end before any real
//	step exists — which is what makes it possible to settle the TUI's layout
//	before implementing 67 probes against it.
//
// Delete this package once real steps cover the same phases. Until then it is
// also the cheapest reproduction of a failing install for renderer work: a
// retry, a downgrade and a hard failure on demand, without a cluster.
package demo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/event"
)

// Options shapes the simulated run.
type Options struct {
	// Nodes are the hosts the node-scoped phases traverse.
	Nodes []string

	// Speed scales every simulated delay. 1 is roughly a ten-second run; 0
	// removes the delays entirely, which is what tests want.
	Speed float64

	// FailAt makes the step with this id fail permanently. Empty runs clean.
	// Use it to look at the failure path: "l1-bootstrap/rke2-server-ready".
	FailAt string

	// FlakyAt makes the step with this id fail twice and then succeed, so the
	// retry counter has something to show.
	FlakyAt string
}

func (o Options) delay(d time.Duration) time.Duration {
	if o.Speed <= 0 {
		return 0
	}
	return time.Duration(float64(d) / o.Speed)
}

// Phases returns a simulated version of the docs/11-execute.md §2 catalogue,
// abbreviated to the phases the mockup shows.
func Phases(o Options) []engine.Phase {
	if len(o.Nodes) == 0 {
		o.Nodes = []string{"10.10.0.11", "10.10.20.21", "10.10.0.22"}
	}
	if o.Speed == 0 {
		o.Speed = 1
	}
	server := o.Nodes[0]

	return []engine.Phase{
		{
			ID: "preflight", Grade: engine.GradeAdditive, Traversal: engine.TraversalParallel,
			Nodes: o.Nodes,
			Steps: func(node string) []engine.Step {
				return []engine.Step{
					step(o, "probe-base", 300*time.Millisecond,
						"PF-101 os release read", "PF-106 systemd 255"),
					step(o, "probe-ebpf", 500*time.Millisecond,
						"PF-201 CONFIG_BPF_SYSCALL present", "PF-204 minimal program loaded"),
					step(o, "probe-network", 400*time.Millisecond,
						"PF-601 port matrix reachable", "PF-604 hostname unique"),
				}
			},
		},
		{
			ID: "plan", Grade: engine.GradeAdditive, Traversal: engine.TraversalCluster,
			Steps: func(string) []engine.Step {
				return []engine.Step{
					step(o, "resolve-profile", 200*time.Millisecond, "profile onprem-dmz resolved"),
					step(o, "decide-dataplane", 400*time.Millisecond,
						"cilium-gw requested", "canal-traefik selected as fallback"),
				}
			},
		},
		{
			ID: "l0-node-prep", Grade: engine.GradeMutating, Traversal: engine.TraversalParallel,
			Nodes: o.Nodes,
			Steps: func(node string) []engine.Step {
				return []engine.Step{
					step(o, "sysctl", 400*time.Millisecond, "applying kernel parameters"),
					step(o, "trust-store", 500*time.Millisecond, "installing CA certificate"),
					step(o, "containerd-registries", 400*time.Millisecond,
						"writing registries.yaml", "mirroring harbor.acme.internal"),
				}
			},
		},
		{
			ID: "l1-bootstrap", Grade: engine.GradeMutating, Traversal: engine.TraversalSequential,
			Nodes: []string{server},
			Steps: func(string) []engine.Step {
				return []engine.Step{
					step(o, "rke2-server-install", 800*time.Millisecond,
						"pulling rke2-runtime image", "writing config.yaml"),
					step(o, "rke2-server-ready", 900*time.Millisecond,
						"starting rke2-server", "waiting for apiserver readiness"),
					step(o, "kubeconfig", 300*time.Millisecond, "writing admin kubeconfig"),
				}
			},
		},
		{
			ID: "l1-join-agent", Grade: engine.GradeMutating, Traversal: engine.TraversalSequential,
			Nodes: o.Nodes[1:],
			Steps: func(string) []engine.Step {
				return []engine.Step{
					step(o, "rke2-agent-install", 600*time.Millisecond, "pulling rke2-runtime image"),
					step(o, "rke2-agent-ready", 700*time.Millisecond, "waiting for node Ready"),
				}
			},
		},
		{
			ID: "l2-dataplane", Grade: engine.GradeMutating, Traversal: engine.TraversalCluster,
			Steps: func(string) []engine.Step {
				return []engine.Step{
					step(o, "gateway-api-crds", 400*time.Millisecond, "applying Gateway API CRDs"),
					step(o, "cni", 900*time.Millisecond, "installing canal", "waiting for daemonset"),
				}
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Simulated step
// ---------------------------------------------------------------------------

func step(o Options, id string, work time.Duration, lines ...string) engine.Step {
	return &fake{opts: o, id: id, work: work, lines: lines}
}

type fake struct {
	opts  Options
	id    string
	work  time.Duration
	lines []string

	applied bool
	tries   int
}

func (f *fake) ID() string { return f.id }

// Observe reports the simulated target state. It is honest about the contract:
// before Apply the state does not hold, afterwards it does, so a second run
// skips everything exactly as a real idempotent step would.
func (f *fake) Observe(ctx context.Context) (engine.Observation, error) {
	if err := sleep(ctx, f.opts.delay(80*time.Millisecond)); err != nil {
		return engine.Observation{}, err
	}
	if f.applied {
		return engine.Observation{Satisfied: true, Detail: f.id + " in the target state"}, nil
	}
	return engine.Observation{Detail: f.id + " not yet applied"}, nil
}

func (f *fake) Apply(ctx context.Context) error {
	f.tries++

	for _, line := range f.lines {
		if err := sleep(ctx, f.opts.delay(f.work/time.Duration(len(f.lines)+1))); err != nil {
			return err
		}
		engine.Logf(ctx, "%s", line)
	}
	if err := sleep(ctx, f.opts.delay(f.work/4)); err != nil {
		return err
	}

	if f.opts.FailAt != "" && f.opts.FailAt == f.id {
		engine.Log(ctx, event.LevelError, "%s failed", f.id)
		return engine.FailFatal("EX-002", fmt.Errorf("simulated failure in %s", f.id))
	}
	if f.opts.FlakyAt != "" && f.opts.FlakyAt == f.id && f.tries < 3 {
		engine.Log(ctx, event.LevelWarn, "%s attempt %d did not take, retrying", f.id, f.tries)
		return engine.Fail("EX-002", fmt.Errorf("simulated transient failure in %s", f.id))
	}

	f.applied = true
	return nil
}

// sleep waits, but gives up as soon as the run is cancelled. A step that
// ignores cancellation makes the whole run unstoppable.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ErrNoSuchStep reports an Options field naming a step that does not exist,
// which would otherwise fail silently by never triggering.
var ErrNoSuchStep = errors.New("demo: no such step")

// Validate checks that FailAt and FlakyAt name real steps.
func Validate(o Options) error {
	ids := map[string]bool{}
	for _, p := range Phases(o) {
		for _, target := range append([]string{""}, p.Nodes...) {
			if p.Steps == nil {
				continue
			}
			for _, s := range p.Steps(target) {
				ids[s.ID()] = true
			}
		}
	}
	for _, want := range []string{o.FailAt, o.FlakyAt} {
		if want != "" && !ids[want] {
			return fmt.Errorf("%w: %q", ErrNoSuchStep, want)
		}
	}
	return nil
}
