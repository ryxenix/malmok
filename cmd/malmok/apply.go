package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"platform.ryxen.dev/malmok/api/v1alpha1"
	"platform.ryxen.dev/malmok/internal/attach"
	"platform.ryxen.dev/malmok/internal/build"
	"platform.ryxen.dev/malmok/internal/demo"
	"platform.ryxen.dev/malmok/internal/engine"
	"platform.ryxen.dev/malmok/internal/event"
	"platform.ryxen.dev/malmok/internal/spec"
	"platform.ryxen.dev/malmok/internal/state"
	"platform.ryxen.dev/malmok/internal/tui"
)

func newApplyCmd() *cobra.Command {
	var (
		specFile string
		bundle   string
		isDemo   bool
		speed    float64
		failAt   string
		flakyAt  string
		recheck  bool
		resume   string
		quiet    bool
		verbose  bool
		output   string
		screen   tuiFlags

		allowLiteral bool
		insecureHost bool
		approve      bool
		timeout      time.Duration
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Run the phases that build a cluster",
		Long: `apply runs the phase catalogue. Every phase is idempotent and every run is
resumable: an interrupted run continues from where it stopped rather than
starting over. See docs/11-execute.md.`,
		Example: `  # simulated run -- no node is contacted, nothing is installed
  malmok apply --demo

  # simulated run that fails, to look at the failure path
  malmok apply --demo --fail-at rke2-server-ready

  # resume an interrupted run
  malmok apply --demo --resume 01JBQ8F2K3M5N7P9R1S3T5V7W9`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isDemo && specFile != "" {
				return runBuild(cmd, buildFlags{
					specFile: specFile, bundle: bundle, resume: resume, recheck: recheck,
					allowLiteral: allowLiteral, insecureHost: insecureHost,
					timeout: timeout, quiet: quiet, verbose: verbose,
					approve: approve, output: output, screen: &screen,
				})
			}
			if !isDemo && !screen.enabled {
				return errors.New(
					"pass -f cluster.yaml, --tui to build one interactively, or --demo for a simulated run")
			}

			opts := demo.Options{Speed: speed, FailAt: failAt, FlakyAt: flakyAt}
			if err := demo.Validate(opts); err != nil {
				return err
			}

			runDir, st, err := openRun(bundle, resume)
			if err != nil {
				return err
			}

			events, err := event.OpenFile(filepath.Join(runDir, "events.jsonl"), st.Run)
			if err != nil {
				return err
			}
			defer events.Close()

			runner := &engine.Runner{
				Events:    events.Writer,
				State:     st,
				StatePath: state.Path(runDir),
				Resume:    state.Options{Recheck: recheck},
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			eventPath := filepath.Join(runDir, "events.jsonl")
			phases := demo.Phases(opts)

			if screen.enabled {
				// The wizard drives the work: preflight first so its findings
				// can be reviewed, then the remaining phases. Both go through
				// the same runner and state file, so resume treats them as one
				// run.
				// The run keeps the document it was built from, so it can be
				// re-run, resumed or handed over on its own (§1.1). Written
				// before the first phase touches anything: a run that fails
				// early still leaves behind what it was trying to do.
				snapshot := func(cfg tui.Config) error {
					return spec.Snapshot(runDir, cfg.ToSpec())
				}
				// One session for both steps: the checks and the install are
				// separate screens but one visit to the same nodes, and
				// opening a second set of connections would let the checks
				// pass on a connection the install then fails to make.
				sess := &build.Session{InsecureHostKey: insecureHost}
				defer sess.Close()

				preflight := func(c context.Context, cfg tui.Config) error {
					if err := snapshot(cfg); err != nil {
						return err
					}
					if isDemo {
						// --demo contacts no node. Running the real checks here
						// would break the one promise the flag makes, and the
						// addresses in the wizard are placeholders nobody owns.
						return runner.Run(c, phases[:1])
					}
					// Results reach the screen as events, never as a return
					// value: ADR-002 keeps the renderer a consumer of the
					// stream, so a run looks the same whether or not anybody
					// was watching it.
					return sess.Preflight(c, cfg.ToSpec(), cfg.SSHPassword, events.Writer)
				}
				install := func(c context.Context, cfg tui.Config) error {
					// Written again: the operator may have gone back and
					// changed something after the checks ran.
					if err := snapshot(cfg); err != nil {
						return err
					}
					if !isDemo {
						// The same pipeline `apply -f` runs, on the same engine,
						// writing the same events. The wizard is a way of
						// producing the document, not a second installer.
						return sess.Install(c, cfg.ToSpec(), cfg.SSHPassword,
							events.Writer, runDir, st, recheck)
					}
					if err := runner.Run(c, phases[1:]); err != nil {
						return err
					}
					// Same artifacts as the real path, which writes them
					// inside sess.Install.
					_, err := writeArtifacts(runDir, output)
					return err
				}
				// The upgrade reads the document the operator chose on screen
				// rather than the one this command was given: an upgrade is a
				// thing done to a cluster that already exists, and the wizard is
				// where that cluster is named.
				upgradeWork := func(c context.Context, cfg tui.Config) error {
					if isDemo {
						return errors.New(
							"--demo contacts no node, so there is no cluster to upgrade")
					}
					chosen, err := spec.Load(cfg.DocPath)
					if err != nil {
						return err
					}
					return sess.Upgrade(c, chosen.Spec, cfg.UpgradeTo, cfg.SSHPassword,
						events.Writer, runDir, st)
				}
				sc, err := screen.screen(ctx, st.Run, runDir, preflight, install, upgradeWork)
				if err != nil {
					return err
				}
				if err := runWithScreen(ctx, sc, eventPath, st.Run); err != nil {
					return err
				}
				if sc.Aborted() {
					return errors.New("aborted")
				}
				return nil
			}

			// Render as we go. The renderer is a consumer of the stream, never a
			// participant: the same file drives `attach` from another terminal,
			// and the run survives this process losing its terminal.
			var sink attach.Sink
			var summary func()
			if quiet {
				sink, summary = &attach.Collector{}, func() {}
			} else {
				r := attach.NewTextRenderer(cmd.OutOrStdout())
				r.Verbose = verbose
				sink, summary = r, func() { fmt.Fprint(cmd.OutOrStdout(), r.Summary()) }
			}
			defer summary()

			fmt.Fprintf(cmd.ErrOrStderr(), "run %s\n  %s\n\n", st.Run, runDir)

			// Even the simulated run leaves its document behind, so the run
			// directory always means the same thing.
			if err := spec.Snapshot(runDir, demoSpec(opts)); err != nil {
				return err
			}

			done := followRun(ctx, eventPath, st.Run, sink)
			runErr := runner.Run(ctx, phases)
			<-done

			// A simulated run leaves the same artifacts a real one does --
			// including a schema-valid handoff whose spec annotation says no
			// node was touched -- so a consumer can be developed against
			// --demo output and believe what it reads.
			if _, err := writeArtifacts(runDir, output); err != nil && runErr == nil {
				return err
			}
			return runErr
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&specFile, "file", "f", "", "cluster.yaml to apply")
	fl.StringVar(&output, "output", "",
		"also write the machine-readable handoff to this path (artifacts/handoff.json is always written)")
	fl.StringVar(&bundle, "bundle", "./out", "bundle path; runs are written under <bundle>/runs/")
	fl.BoolVar(&isDemo, "demo", false, "simulate a run without contacting any node")
	fl.Float64Var(&speed, "speed", 1, "scale simulated delays; higher is faster")
	fl.StringVar(&failAt, "fail-at", "", "make this step id fail permanently")
	fl.StringVar(&flakyAt, "flaky-at", "", "make this step id fail twice, then succeed")
	fl.BoolVar(&recheck, "recheck", false, "re-observe steps already recorded as done")
	fl.StringVar(&resume, "resume", "", "resume the run with this id")
	fl.BoolVar(&quiet, "quiet", false, "emit events to the file only")
	fl.BoolVarP(&verbose, "verbose", "v", false, "include log lines")
	fl.BoolVar(&allowLiteral, "allow-literal-secrets", false,
		"permit literal:// on secret fields (cluster.yaml is handed to customers)")
	fl.BoolVar(&insecureHost, "insecure-host-key", false,
		"accept any SSH host key; freshly installed nodes have no known_hosts entry yet")
	fl.DurationVar(&timeout, "timeout", 60*time.Minute, "give up on the whole build after this long")
	fl.BoolVar(&approve, "approve", false,
		"proceed without asking, even where a phase reconfigures a running cluster")
	screen.register(cmd)

	return cmd
}

// openRun creates a run directory, or reopens one to resume.
func openRun(bundle, resumeID string) (string, *state.State, error) {
	if resumeID != "" {
		dir := filepath.Join(bundle, "runs", resumeID)
		st, err := state.Load(state.Path(dir))
		if err != nil {
			return "", nil, err
		}
		return dir, st, nil
	}

	id := newRunID()
	dir := filepath.Join(bundle, "runs", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create run directory: %w", err)
	}
	// The demo has no cluster.yaml, so the digest covers what shapes the run.
	st := state.New(id, state.Digest([]byte("demo")), time.Now())
	return dir, st, nil
}

// newRunID returns a lexically sortable, time-ordered identifier.
//
// Crockford base32 over a millisecond timestamp and random tail: the same
// ordering property a ULID gives, without a dependency for one function.
func newRunID() string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	ms := uint64(time.Now().UTC().UnixMilli())
	buf := make([]byte, 26)
	for i := 9; i >= 0; i-- {
		buf[i] = alphabet[ms&0x1f]
		ms >>= 5
	}
	for i := 10; i < 26; i++ {
		buf[i] = alphabet[rand.IntN(32)]
	}
	return string(buf)
}

// followRun tails the run's own event file so the terminal shows what the file
// records, rather than a second rendering path that could disagree with it.
func followRun(ctx context.Context, path, run string, sink attach.Sink) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f := &attach.Follower{
			Path: path, Run: run, StopOnRunEnd: true,
			PollInterval: 50 * time.Millisecond,
		}
		_ = f.Follow(ctx, sink)
	}()
	return done
}

// demoSpec is what the simulated run would have been built from. It exists so
// that a run directory always contains the same set of files, whether the run
// was real or not -- a directory that is sometimes missing its document is one
// nobody can write a handover procedure against.
func demoSpec(o demo.Options) v1alpha1.ClusterSpec {
	nodes := o.Nodes
	if len(nodes) == 0 {
		nodes = []string{"10.10.0.11", "10.10.20.21", "10.10.0.22"}
	}
	// A profile, so the baseline fills in everything a document needs and the
	// snapshot is one that can actually be re-read. The annotation is what
	// tells a reader six months later that no node was ever touched.
	s := v1alpha1.ClusterSpec{
		APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindSpec,
		Metadata: v1alpha1.Metadata{
			Name:        "demo",
			Profile:     v1alpha1.ProfileHomelab,
			Annotations: map[string]string{"platform.ryxen.dev/simulated": "true"},
		},
		Topology: v1alpha1.TopologySpec{
			RegistrationAddress: "k8s-api.demo.invalid",
			Servers:             []v1alpha1.NodeSpec{{Host: nodes[0], Role: v1alpha1.RoleServer}},
		},
		Kubernetes: v1alpha1.KubernetesSpec{
			Version: "v1.34.5+rke2r1",
			// homelab uses cilium-gw, and Cilium LB-IPAM needs somewhere to
			// take the gateway address from.
			Dataplane: v1alpha1.DataplaneSpec{LoadBalancerPool: []string{"10.10.20.240/29"}},
		},
	}
	for _, n := range nodes[1:] {
		s.Topology.Agents = append(s.Topology.Agents,
			v1alpha1.NodeSpec{Host: n, Role: v1alpha1.RoleAgent})
	}
	return s
}
