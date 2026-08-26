//go:build lab

// Package lab runs the verification matrix against real nodes.
//
// Behind a build tag because it needs machines it is allowed to destroy: every
// case wipes both nodes back to a bare OS before it builds. `go test ./...`
// never picks it up; `scripts/matrix.sh` does, deliberately, against the
// throwaway segment.
//
// It drives the built binary rather than the engine's packages. What an
// operator runs is `malmok apply -f cluster.yaml`, and a harness that called
// the engine directly would verify a path nobody uses.
package lab

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sshexec "github.com/ryxen/malmok/internal/exec"
	"github.com/ryxen/malmok/internal/matrix"
	"github.com/ryxen/malmok/internal/rke2"
	"github.com/ryxen/malmok/internal/spec"
)

// The lab it runs against. Addresses come from the environment so the suite
// can be pointed at a different pair without an edit.
var (
	server   = env("MALMOK_LAB_SERVER", "192.168.88.241")
	agent    = env("MALMOK_LAB_AGENT", "192.168.88.244")
	user     = env("MALMOK_LAB_USER", "k8s")
	password = os.Getenv("NODE_PASSWORD")
	binary   = env("MALMOK_BIN", "../../bin/malmok")
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestMatrix(t *testing.T) {
	if password == "" {
		t.Fatal("NODE_PASSWORD is unset; the harness needs the lab account's password")
	}
	bin, err := filepath.Abs(binary)
	if err != nil || !exists(bin) {
		t.Fatalf("no binary at %s: build it with scripts/build.sh", binary)
	}

	// The version comes from the channel server, not from a constant. A suite
	// that pins a version stops testing the version people install.
	ch, err := rke2.FetchChannels(context.Background())
	if err != nil || ch.Stable == "" {
		t.Fatalf("could not read the RKE2 channel: %v", err)
	}
	t.Logf("matrix on %s: %d cases, RKE2 stable %s, latest %s",
		server, len(matrix.Cases()), ch.Stable, ch.Latest)

	// Sequential by necessity: every case owns the same two machines.
	for _, c := range matrix.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			t.Logf("%s -- %s", c, c.Why)
			// Not t.TempDir(): a failed case's run directory is the one
			// thing worth keeping, and t.TempDir() deletes exactly that --
			// twice in a row a failure was diagnosed by guesswork because the
			// evidence the tool had written went with it.
			dir, err := os.MkdirTemp("", "malmok-matrix-"+c.Name+"-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("the run is kept at %s", dir)
					return
				}
				_ = os.RemoveAll(dir)
			})

			run := &labRun{t: t, bin: bin, version: ch.Stable, newer: ch.Latest, dir: dir}
			run.wipe()
			run.execute(c)
		})
	}
}

type labRun struct {
	t   *testing.T
	bin string
	// version is what a case builds at; newer is where an upgrade case moves
	// to. Both come from the channel server, so the suite follows what people
	// actually install rather than a constant somebody has to remember.
	version string
	newer   string
	dir     string
}

// execute builds the case and then does to it whatever its operation says.
func (r *labRun) execute(c matrix.Case) {
	doc := r.write(c, false)

	switch c.Op {
	case matrix.OpBuild:
		r.apply(doc, 30*time.Minute)

	case matrix.OpGrow:
		r.apply(doc, 30*time.Minute)
		// The second node arrives in the document, not in a different command.
		r.apply(r.write(c, true), 30*time.Minute)
		r.expectNodes(2)

	case matrix.OpResume:
		r.applyThenKill(doc, "l1-bootstrap/service")
		r.apply(doc, 30*time.Minute)

	case matrix.OpUpgrade:
		// An upgrade needs somewhere to go. When the channels have converged
		// there is no newer version to move to, and skipping says so rather
		// than passing on a run that did nothing.
		if r.newer == "" || r.newer == r.version {
			r.t.Skipf("stable and latest are both %s, so there is no upgrade to make", r.version)
		}
		r.apply(doc, 30*time.Minute)
		r.upgrade(doc, r.newer)
		// The observable is the version the kubelets report, not the version
		// the document asks for: a document is a request and a running
		// kubelet is the answer.
		r.expectVersion(r.newer)

	case matrix.OpReapply:
		r.apply(doc, 30*time.Minute)
		// A second run of the same document must observe and skip. Anything
		// it applies again is work it did not need to do, and on a live
		// cluster that is a restart nobody asked for.
		out := r.apply(doc, 20*time.Minute)
		if applied := countApplied(out); applied > 0 {
			r.t.Errorf("a re-apply changed %d step(s); it should have observed and skipped every one", applied)
		}
	}

	r.expectHealthy(c)
}

// write renders the case's document into the run's directory, with whatever
// certificate material its mode needs.
func (r *labRun) write(c matrix.Case, grown bool) string {
	r.t.Helper()

	var m matrix.Material
	if c.PKI == "private-ca" || c.PKI == "byo-cert" {
		m = matrix.Material(writeMaterial(r.t, r.dir))
	}
	doc := c.Document(r.version, matrix.Hosts{
		Server: server, Agent: agent, User: user, PasswordRef: "env://NODE_PASSWORD",
	}, m, grown)

	path := filepath.Join(r.dir, "cluster.yaml")
	if err := spec.Save(path, doc); err != nil {
		r.t.Fatalf("write the document: %v", err)
	}
	return path
}

// apply runs the tool the way an operator does, and returns its output.
func (r *labRun) apply(doc string, timeout time.Duration) string {
	r.t.Helper()
	out, err := r.malmok(timeout, "apply", "-f", doc, "--insecure-host-key", "--approve",
		"--timeout", timeout.String(), "--bundle", filepath.Join(r.dir, "out"))
	if err != nil {
		r.t.Fatalf("apply: %v\n%s", err, tail(out, 40))
	}
	return out
}

// upgrade moves the cluster, one node at a time, the way an operator does.
func (r *labRun) upgrade(doc, to string) {
	r.t.Helper()
	out, err := r.malmok(45*time.Minute, "upgrade", "-f", doc, "--to", to,
		"--insecure-host-key", "--approve", "--timeout", "45m",
		"--bundle", filepath.Join(r.dir, "out"))
	if err != nil {
		r.t.Fatalf("upgrade to %s: %v\n%s", to, err, tail(out, 40))
	}
}

// expectVersion asks every node what it is running.
func (r *labRun) expectVersion(want string) {
	r.t.Helper()
	r.onNode(fmt.Sprintf(
		`wrong=$(kubectl get nodes --no-headers -o custom-columns=N:.metadata.name,V:.status.nodeInfo.kubeletVersion | awk '$2!="%s"'); `+
			`[ -z "$wrong" ] || { echo "$wrong"; exit 1; }`, want))
}

// applyThenKill starts a build and kills it when the named step begins, which
// is what a power cut looks like from the tool's side.
func (r *labRun) applyThenKill(doc, step string) {
	r.t.Helper()

	log := filepath.Join(r.dir, "killed.log")
	f, err := os.Create(log)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()

	cmd := exec.Command(r.bin, "apply", "-f", doc, "--insecure-host-key", "--approve",
		"--timeout", "30m", "--bundle", filepath.Join(r.dir, "out"))
	cmd.Env = append(os.Environ(), "NODE_PASSWORD="+password)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(log)
		if strings.Contains(string(body), step) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			r.t.Logf("killed the run at %s", step)
			return
		}
		time.Sleep(2 * time.Second)
	}
	_ = cmd.Process.Kill()
	r.t.Fatalf("%s never started, so there was nothing to interrupt", step)
}

// expectHealthy asks the cluster what it is, from the operator's own account.
func (r *labRun) expectHealthy(c matrix.Case) {
	r.t.Helper()

	nodes := c.Nodes
	if c.Op == matrix.OpGrow {
		nodes = 2
	}
	r.expectNodes(nodes)

	// The tools the kubeconfig is for. A cluster whose kubectl is buried in
	// /var/lib/rancher looks broken from the machine it was built on.
	r.onNode("command -v kubectl >/dev/null || { echo no-kubectl; exit 1; }")

	if c.Exposure != "none" && strings.HasPrefix(c.Dataplane, "cilium") && c.Dataplane == "cilium-gw" {
		r.onNode(`kubectl get gatewayclass cilium -o jsonpath='{range .status.conditions[?(@.type=="Accepted")]}{.status}{end}' | grep -qx True`)
	}
	// Every pod the case installed settles into Running or Completed.
	//
	// Settles, not "is": a pod three seconds into ContainerCreating is not a
	// finding, and sampling once turns the tail of a rollout into a failure --
	// the same mistake the clock check made. Pending is still not tolerated
	// once things have settled: a pod that cannot be scheduled is one this
	// tool asked for and the cluster cannot give.
	r.onNode(`for i in $(seq 1 30); do
  bad=$(kubectl get pods -A --no-headers | awk '$4!="Running" && $4!="Completed"' | head -5)
  [ -z "$bad" ] && exit 0
  sleep 10
done
echo "$bad"; exit 1`)
}

func (r *labRun) expectNodes(want int) {
	r.t.Helper()
	r.onNode(fmt.Sprintf(
		`n=$(kubectl get nodes --no-headers | awk '$2=="Ready"' | wc -l); [ "$n" = %d ] || { kubectl get nodes; exit 1; }`, want))
}

// onNode runs a check on the server as the operator's account -- unelevated,
// because "can the person who built it use it" is part of the answer.
func (r *labRun) onNode(script string) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runner, err := sshexec.Connect(ctx, sshexec.SSHConfig{
		Host: server, User: user, Password: password, InsecureSkipHostKeyCheck: true,
	})
	if err != nil {
		r.t.Fatalf("connect: %v", err)
	}
	defer runner.Close()

	res, err := runner.Run(ctx, script)
	if err != nil || !res.OK() {
		r.t.Errorf("check failed (exit %d): %s\n%s\n%s", res.ExitCode, script, res.Out(), res.Err())
	}
}

// wipe returns both machines to a bare OS.
//
// The reboot is not decoration: rke2-uninstall leaves Cilium's tc/eBPF
// programs attached to the NIC, and they drop peer traffic on the next build
// -- which the port matrix then reports as a firewall.
func (r *labRun) wipe() {
	r.t.Helper()
	for _, host := range []string{agent, server} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		runner, err := sshexec.Connect(ctx, sshexec.SSHConfig{
			Host: host, User: user, Password: password, InsecureSkipHostKeyCheck: true,
		})
		if err != nil {
			cancel()
			r.t.Fatalf("connect %s: %v", host, err)
		}
		root, err := sshexec.Elevate(ctx, runner, password)
		if err != nil {
			cancel()
			r.t.Fatalf("elevate on %s: %v", host, err)
		}
		if _, err := root.Run(ctx, wipeScript); err != nil {
			r.t.Fatalf("wipe %s: %v", host, err)
		}
		_, _ = root.Run(ctx, `nohup sh -c 'sleep 1; reboot' >/dev/null 2>&1 &`)
		runner.Close()
		cancel()
	}
	r.waitForNodes()
}

const wipeScript = `
[ -x /usr/local/bin/rke2-killall.sh ] && /usr/local/bin/rke2-killall.sh >/dev/null 2>&1 || true
[ -x /usr/local/bin/rke2-uninstall.sh ] && /usr/local/bin/rke2-uninstall.sh >/dev/null 2>&1 || true
rm -f /usr/local/bin/kubectl /usr/local/bin/k9s
rm -rf /etc/rancher /var/lib/rancher /var/lib/kubelet /etc/cni /opt/cni /var/lib/cni
rm -f /root/.kube/config /home/*/.kube/config
echo wiped`

func (r *labRun) waitForNodes() {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for _, host := range []string{server, agent} {
		for {
			if time.Now().After(deadline) {
				r.t.Fatalf("%s did not come back after the wipe", host)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			runner, err := sshexec.Connect(ctx, sshexec.SSHConfig{
				Host: host, User: user, Password: password, InsecureSkipHostKeyCheck: true,
			})
			cancel()
			if err == nil {
				runner.Close()
				break
			}
			time.Sleep(5 * time.Second)
		}
	}
}

func (r *labRun) malmok(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env = append(os.Environ(), "NODE_PASSWORD="+password)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// countApplied counts the steps a run actually changed. The renderer marks an
// observed-and-skipped step with "--" and an applied one with "OK".
func countApplied(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " step ") && strings.Contains(line, "OK  step") {
			n++
		}
	}
	return n
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
