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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	sshexec "github.com/ryxenix/malmok/internal/exec"
	"github.com/ryxenix/malmok/internal/matrix"
	"github.com/ryxenix/malmok/internal/rke2"
	"github.com/ryxenix/malmok/internal/spec"
)

// The lab it runs against.
//
// No default addresses, and that is the point: this suite wipes and reboots
// both machines before every case. A default here is a loaded gun pointed at
// whatever happens to live at that address on somebody else's network --
// scripts/matrix.sh has said so for as long as it has existed, and then the
// harness carried defaults anyway, so running `go test -tags lab` directly
// went around the safety the script documented.
//
// The account name is a different matter: naming the wrong user fails to
// connect, which costs a message rather than a machine.
var (
	server = os.Getenv("MALMOK_LAB_SERVER")
	agent  = os.Getenv("MALMOK_LAB_AGENT")

	// third is the machine a three-server case adds. Optional: only the HA
	// case needs it, and that case skips without one. When it is set it is
	// wiped with the other two before every case -- a server left over from
	// the HA case keeps announcing the VIP and would fight the next case's
	// kube-vip for it.
	third    = os.Getenv("MALMOK_LAB_THIRD")
	user     = env("MALMOK_LAB_USER", "k8s")
	password = os.Getenv("NODE_PASSWORD")
	binary   = env("MALMOK_BIN", "../../bin/malmok")

	// airgapVersion is the RKE2 release the artifacts staged on the nodes
	// carry. It cannot be read from them -- the archive names hold no version
	// -- and it cannot be the channel's newest, because the artifacts are
	// 1.3GB and are staged out of band rather than downloaded per run. Unset,
	// the air-gapped case skips and says what to stage.
	airgapVersion = os.Getenv("MALMOK_LAB_AIRGAP_VERSION")

	// airgapFrom is the release the lifecycle case builds at before upgrading
	// to airgapVersion. Both are staged on every node: airgapVersion's in the
	// artifact path, or beside it as <path>-<version>, and the same for this
	// one. The harness swaps the two directories by name -- they are 2GB each
	// and are never copied per run -- and reads which release a directory
	// holds from the rke2 binary inside its tarball, the way UP-006 does.
	// Unset, the lifecycle case skips.
	airgapFrom = os.Getenv("MALMOK_LAB_AIRGAP_FROM")

	// mirror is the address of the pull-through cache in
	// test/lab/cache/compose.yaml, or "" for no cache.
	//
	// Every case wipes both nodes, which takes containerd's image store with
	// it, so the platform's images are fetched from the internet again for
	// every case. Pointing the nodes at a cache on the segment makes that the
	// first case's cost rather than each one's.
	//
	// It is off by default and has to be named, for two reasons. A cache is a
	// second thing that can be wrong, and a run that passes through one has
	// not shown that a customer's first install works. And it changes the
	// document: the mirror path is worth exercising -- registry.mirrors is in
	// the schema and has never been run against hardware -- but a green matrix
	// with it on is not a green matrix.
	mirror = os.Getenv("MALMOK_LAB_MIRROR")

	// Addresses on the nodes' own segment, for the cases that pin one.
	//
	// They cannot be the documentation ranges the matrix defaults to: kube-vip
	// claims the VIP on an interface, and no interface is on 192.0.2.0/24, so
	// every case with a VIP failed at vip-interface saying precisely that.
	//
	// Unset here rather than guessed, for the same reason the node addresses
	// are. Both must be free on the segment and outside whatever hands out
	// leases, and only whoever owns the segment knows which those are; the
	// cases that need them skip and say so.
	vip    = os.Getenv("MALMOK_LAB_VIP")
	lbPool = os.Getenv("MALMOK_LAB_LB_POOL")
)

// lbAddress is the address a gateway is pinned to: the first in the pool.
func lbAddress() string {
	if i := strings.Index(lbPool, "/"); i > 0 {
		return lbPool[:i]
	}
	return lbPool
}

// cachePorts maps an upstream registry to the port test/lab/cache/compose.yaml
// serves it on. One registry per upstream, because a pull-through cache
// proxies exactly one remote.
var cachePorts = map[string]int{
	"docker.io":       5100,
	"registry.k8s.io": 5101,
	"quay.io":         5102,
	"ghcr.io":         5103,
}

// mirrors is what to put in the document, or nil when no cache was named.
//
// http, not https: the cache holds public images on a lab segment, and a
// certificate would be a certificate for an address, presented to nodes that
// are wiped between cases.
func mirrors() map[string][]string {
	if mirror == "" {
		return nil
	}
	m := make(map[string][]string, len(cachePorts))
	for upstream, port := range cachePorts {
		m[upstream] = []string{fmt.Sprintf("http://%s:%d", mirror, port)}
	}
	return m
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestMatrix(t *testing.T) {
	// Named machines only. Every case wipes and reboots both of them, so the
	// suite refuses to guess which two -- scripts/matrix.sh enforces the same
	// thing, and this is the path that goes around the script.
	if server == "" || agent == "" {
		t.Skip("set MALMOK_LAB_SERVER and MALMOK_LAB_AGENT to two machines you are " +
			"willing to lose: every case wipes and reboots both of them")
	}
	if password == "" {
		t.Fatal("NODE_PASSWORD is unset; the harness needs the lab account's password")
	}
	bin, err := filepath.Abs(binary)
	if err != nil || !exists(bin) {
		t.Fatalf("no binary at %s: build it with scripts/build.sh", binary)
	}

	// The version comes from the channel server, not from a constant. A suite
	// that pins a version stops testing the version people install.
	ch, older, source := labVersions(t)
	t.Logf("matrix on %s: %d cases, RKE2 stable %s, latest %s, upgrades from %s (%s)",
		server, len(matrix.Cases()), ch.Stable, ch.Latest, orNone(older), source)

	// Sequential by necessity: every case owns the same machines.
	for _, c := range matrix.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			t.Logf("%s -- %s", c, c.Why)
			if why := notRunnable(c); why != "" {
				t.Skip(why)
			}
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

			run := &labRun{t: t, bin: bin, version: ch.Stable, newer: ch.Latest,
				older: older, dir: dir}
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
	// older is the release before version's minor, which is where an upgrade
	// case starts. Without it the case had nowhere to come from whenever the
	// channels had converged, and skipped.
	older string
	dir   string
	// airgapAt is the release an air-gapped case's document asks for and its
	// nodes' artifact path holds: airgapVersion, or airgapFrom for the
	// lifecycle case until it upgrades.
	airgapAt string
}

// execute builds the case and then does to it whatever its operation says.
func (r *labRun) execute(c matrix.Case) {
	if c.Network == "airgap" {
		r.airgapAt = airgapVersion
		if c.Op == matrix.OpLifecycle {
			r.airgapAt = airgapFrom
		}
		defer r.prepareAirgap(c)()
	}
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
		// An upgrade needs somewhere to come from, and stable is the wrong end
		// to start at: stable and latest converge for weeks at a time, and
		// this case then skipped every run -- which is how the README came to
		// cite the verification matrix as evidence for a row the matrix was
		// not exercising.
		//
		// So it builds at the previous minor and moves to what people install
		// today, which is also the upgrade an operator actually performs.
		if r.older == "" {
			r.t.Skip("the channel server named no release before " + r.version +
				", so there is nothing to upgrade from")
		}
		r.version = r.older
		doc = r.write(c, false)
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

	case matrix.OpFailover:
		r.apply(doc, 30*time.Minute)
		// Three etcd members before anything is taken away: a cluster that
		// was never a quorum cannot show that it survives losing part of one.
		r.onNode(fmt.Sprintf(`n=$(kubectl get nodes -l node-role.kubernetes.io/etcd=true --no-headers | awk '$2=="Ready"' | wc -l); `+
			`[ "$n" = %d ] || { kubectl get nodes; exit 1; }`, c.Nodes))
		r.failover()

	case matrix.OpLifecycle:
		r.apply(doc, 40*time.Minute)
		r.expectVersion(airgapFrom)
		r.onNode(fmt.Sprintf(`n=$(kubectl get nodes -l node-role.kubernetes.io/etcd=true --no-headers | awk '$2=="Ready"' | wc -l); `+
			`[ "$n" = %d ] || { kubectl get nodes; exit 1; }`, c.Nodes))
		r.failover()
		r.upgradeLosingArtifacts(c, doc)
		r.restoreAll(c)
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
	version := r.version
	if c.Network == "airgap" {
		// The nodes hold one release. Asking for another is a document that
		// cannot be satisfied without a network, which is the one thing this
		// case does not have.
		version = r.airgapAt
	}
	doc := c.Document(version, matrix.Hosts{
		Server: server, Agent: agent, Third: third, User: user, PasswordRef: "env://NODE_PASSWORD",
		VIP: vip, LBPool: lbPool, LBAddress: lbAddress(),
	}, m, grown)

	// The cache lives on the segment, and the air-gapped case leaves the
	// segment reachable -- it drops everything else. Pointing that case at a
	// cache would give it somewhere to pull from, which is the one thing it
	// exists to prove the node does not need.
	if m := mirrors(); m != nil && c.Network != "airgap" {
		doc.Registry.Mirrors = m
	}

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

	// The run exiting on its own is the interesting case, and waiting fifteen
	// minutes to call it "never started" hides it. That is what happened when
	// ca-trust halted every private-CA run in l0: this case spent its whole
	// budget watching a process that had already failed, and reported a step
	// that never began rather than the failure that stopped it.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(log)
		if strings.Contains(string(body), step) {
			_ = cmd.Process.Kill()
			<-done
			r.t.Logf("killed the run at %s", step)
			return
		}
		select {
		case err := <-done:
			r.t.Fatalf("the run ended before %s began (%v):\n%s",
				step, err, tail(readFile(log), 30))
		case <-time.After(2 * time.Second):
		}
	}
	_ = cmd.Process.Kill()
	<-done
	r.t.Fatalf("%s never started within 15m, so there was nothing to interrupt:\n%s",
		step, tail(readFile(log), 30))
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

	r.expectCertificates()
}

// expectCertificates measures the cluster's own certificates and requires an
// answer.
//
// Hung off every case rather than given a row of its own: a certificate scan
// is not an operation on a cluster, and the dimension it would have added
// covers less than running it against all eleven shapes does.
//
// Three things are checked, and each one shipped broken. A scan that could
// not read the directory exited zero saying "none near expiry", so the count
// has to be non-zero. A top-level glob missed everything RKE2 keeps in a
// subdirectory -- etcd's five above all, whose expiry stops a cluster hardest
// -- and a count alone cannot see that, so etcd has to appear by name.
func (r *labRun) expectCertificates() {
	r.t.Helper()

	out, err := r.malmok(3*time.Minute, "certs",
		"-f", filepath.Join(r.dir, "cluster.yaml"),
		"--insecure-host-key", "--timeout", "3m",
		"--bundle", filepath.Join(r.dir, "out"), "-v")
	if err != nil {
		// Non-zero is 2 (expiring) or 3 (not measured). On a cluster built
		// minutes ago neither is an acceptable answer.
		r.t.Errorf("certs: %v\n%s", err, tail(out, 25))
		return
	}
	if strings.Count(out, "MC-1") == 0 {
		r.t.Errorf("certs measured nothing on a cluster that was just built:\n%s", tail(out, 25))
	}
	for _, want := range []string{"CN=etcd-server", "CN=etcd-peer-ca"} {
		if !strings.Contains(out, want) {
			r.t.Errorf("certs never reached %s, so it is not descending into "+
				"server/tls subdirectories:\n%s", want, tail(out, 30))
		}
	}
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
	// Longer than the longest wait any caller runs. At two minutes the
	// connection cut the pod-settle loop off at two while it said it waited
	// five -- harmless while pods came up quickly, not after a server has
	// been taken away and every pod on it has to come back.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
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

	// Read before rebooting: after, there is nothing to compare against.
	before := map[string]string{}
	for _, host := range machines() {
		before[host] = r.bootID(host)
	}

	all := machines()
	for i := len(all) - 1; i >= 0; i-- {
		host := all[i]
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
	r.waitForNodes(before)
	r.waitForClocks()
}

const wipeScript = `
[ ! -f /etc/systemd/system/malmok-airgap.service ] || systemctl disable --now --quiet malmok-airgap.service
rm -f /etc/systemd/system/malmok-airgap.service /etc/malmok-airgap.nft /var/lib/malmok-airgap.since
[ -x /usr/local/bin/rke2-killall.sh ] && /usr/local/bin/rke2-killall.sh >/dev/null 2>&1 || true
[ -x /usr/local/bin/rke2-uninstall.sh ] && /usr/local/bin/rke2-uninstall.sh >/dev/null 2>&1 || true
rm -f /usr/local/bin/kubectl /usr/local/bin/k9s
rm -rf /etc/rancher /var/lib/rancher /var/lib/kubelet /etc/cni /opt/cni /var/lib/cni
rm -f /root/.kube/config /home/*/.kube/config
echo wiped`

// waitForClocks waits until every node says its clock is synchronised, and no
// longer.
//
// A node that has just rebooted can sit a second or more off its source for
// most of a minute, and the build's first act is PF-502, which refuses a skew
// over a second. Started straight after the reboot, a run on the lab was
// refused exactly that way: the harness's timing, reported as the cluster's
// fault. A fixed pause would either waste minutes or not be long enough, so
// this asks. chrony answers through `chronyc waitsync`, which returns the
// moment the correction is under 100ms; timesyncd through its offset, polled
// once a second. Both lab time daemons are in use -- the first attempt at this
// read only timesyncd's offset, and on the chrony nodes it waited five minutes
// for a number that never came.
//
// 100ms, not tighter. PF-502 refuses a skew of a second between nodes, and a
// node whose source is another node that has just rebooted too spends minutes
// slewing its last fraction of a second: asked for 10ms, the lab agent was
// still 0.18s out after two minutes and the run stopped for nothing.
func (r *labRun) waitForClocks() {
	r.t.Helper()
	for _, host := range machines() {
		out, err := r.rootRun(host, clockScript, 4*time.Minute)
		if err != nil {
			r.t.Fatalf("%s's clock did not synchronise after the reboot: %v\n%s", host, err, out)
		}
		r.t.Logf("%s: %s", host, out)
	}
}

const clockScript = `if command -v chronyc >/dev/null 2>&1 && systemctl is-active --quiet chrony; then
  if chronyc waitsync 180 0.1 0 1 >/dev/null; then
    echo "clock synced (chrony, $(chronyc tracking | awk -F': ' '/System time/{print $2}'))"; exit 0
  fi
  echo "not synced after 180s (chrony)"; chronyc tracking; exit 1
fi
for i in $(seq 1 180); do
  off=$(timedatectl timesync-status 2>/dev/null | awk '/Offset:/{print $2}')
  case "$off" in
    *us|*ns) echo "clock synced (timesyncd, offset $off)"; exit 0 ;;
    *ms) v=${off%ms}; v=${v#[+-]}; case "$v" in [0-9].*|[0-9]|[0-9][0-9].*|[0-9][0-9]) echo "clock synced (timesyncd, offset $off)"; exit 0 ;; esac ;;
  esac
  sleep 1
done
echo "not synced after 180s (timesyncd, offset ${off:-unknown})"; exit 1`

// waitForNodes waits until every node has actually rebooted.
//
// Connecting successfully is not the test. The reboot is issued and returns at
// once, so a node that has not begun shutting down answers immediately and
// this used to return before anything had happened -- and the next thing to
// touch that node met it going down. The boot id is the observable: it is a
// different string on the other side of a reboot and on no other occasion.
func (r *labRun) waitForNodes(before map[string]string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for _, host := range machines() {
		for {
			if time.Now().After(deadline) {
				r.t.Fatalf("%s did not come back after the wipe", host)
			}
			if id := r.bootID(host); id != "" && id != before[host] {
				break
			}
			time.Sleep(5 * time.Second)
		}
	}
}

// bootID reads /proc/sys/kernel/random/boot_uuid, or "" when the node cannot
// be asked -- which during a reboot is most of the time and is not an error.
func (r *labRun) bootID(host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runner, err := sshexec.Connect(ctx, sshexec.SSHConfig{
		Host: host, User: user, Password: password, InsecureSkipHostKeyCheck: true,
	})
	if err != nil {
		return ""
	}
	defer runner.Close()

	res, err := runner.Run(ctx, "cat /proc/sys/kernel/random/boot_id 2>/dev/null || true")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Out())
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

// airgapURLs resolves the chart archives a registry-free site carries.
//
// Through each repository's index.yaml, the way helm does. Constructing the URL
// from the repository and the chart name was tried first and worked for
// exactly one of the three: jetstack serves <repo>/charts/<name>-<ver>.tgz and
// the other two publish theirs as GitHub release assets. The index is where a
// repository states that, so it is what gets read.
//
// The versions come from the binary under test, so a bump in Go cannot leave
// the harness staging last release's charts and calling the result verified.
func airgapURLs(t *testing.T, bin string) map[string]string {
	t.Helper()
	out, err := exec.Command(bin, "images", "--charts").Output()
	if err != nil {
		t.Fatalf("ask the binary which charts it pins: %v", err)
	}

	type entry struct {
		Version string   `yaml:"version"`
		URLs    []string `yaml:"urls"`
	}
	type index struct {
		Entries map[string][]entry `yaml:"entries"`
	}

	indexes := map[string]index{}
	urls := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		name, version, repo := f[0], f[1], strings.TrimSuffix(f[2], "/")

		idx, ok := indexes[repo]
		if !ok {
			resp, err := (&http.Client{Timeout: 60 * time.Second}).Get(repo + "/index.yaml")
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("read the chart index at %s: %v", repo, err)
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("read the chart index at %s: %v", repo, err)
			}
			if err := yaml.Unmarshal(b, &idx); err != nil {
				t.Fatalf("parse the chart index at %s: %v", repo, err)
			}
			indexes[repo] = idx
		}

		var found string
		for _, e := range idx.Entries[name] {
			if e.Version == version && len(e.URLs) > 0 {
				found = e.URLs[0]
			}
		}
		if found == "" {
			t.Fatalf("%s %s is not in the index at %s; this release pins a version its repository does not publish",
				name, version, repo)
		}
		if !strings.Contains(found, "://") {
			found = repo + "/" + strings.TrimPrefix(found, "/")
		}
		urls[name+"-"+version+".tgz"] = found
	}
	return urls
}

// prepareAirgap gets the nodes into the state a closed site is in, and returns
// the function that undoes it.
//
// The nodes -- and their pods, see airgapOn -- are cut off from everything but
// the segment with DROP rather than REJECT. The difference is not cosmetic: a rejected connection fails at once
// and a dropped one fails at the connect timeout, so a step that reaches for
// the internet looks fine against the first and hangs against the second --
// and the second is what a site firewall does.
//
// The machine running this is not cut off. That is the real shape too: a
// staging machine has a network, the nodes do not.
func (r *labRun) prepareAirgap(c matrix.Case) func() {
	r.t.Helper()

	if airgapVersion == "" {
		r.t.Skipf("set MALMOK_LAB_AIRGAP_VERSION to the RKE2 release staged in %s on both nodes; "+
			"an air-gapped install reads its binaries and images from there and cannot fetch a "+
			"different version", c.ArtifactPath)
	}

	// The release the case builds at goes into the artifact path first: the
	// lifecycle case leaves the newer one there, and the next case would
	// otherwise build from whatever the last one finished on.
	for _, host := range caseHosts(c) {
		r.t.Logf("%s: %s", host, r.stageRelease(host, c.ArtifactPath, r.airgapAt))
	}

	// The artifacts are staged out of band -- they are 1.3GB and survive a
	// wipe -- so this asks rather than copies, and says exactly what is
	// missing rather than failing later inside the install.
	for _, host := range caseHosts(c) {
		r.onHost(host, fmt.Sprintf(
			`d=%s
[ -d "$d" ] || { echo "no artifact directory at $d; stage the RKE2 release into it"; exit 1; }
for f in rke2.linux-amd64.tar.gz sha256sum-amd64.txt install.sh \
         rke2-images.linux-amd64.tar.zst rke2-images-cilium.linux-amd64.tar.zst \
         gateway-api-v1.4.1-standard-install.yaml; do
  [ -f "$d/$f" ] || { echo "$d is missing $f"; exit 1; }
done`, c.ArtifactPath))
	}

	// The platform images. RKE2's own artifacts carry RKE2; the charts this
	// tool installs pull twenty more, and on a closed node they have to be
	// there before anything asks for them -- the storage provisioner first,
	// which is where the air-gapped case failed once storage existed at all.
	//
	// Staged beside the RKE2 artifacts, which is what an operator carrying
	// both does, and copied into the directory containerd imports from. Here
	// rather than out of band, because the wipe removes /var/lib/rancher
	// entirely: anything put there before the case is gone by the time the
	// case runs.
	//
	// Copied under a name that sorts after RKE2's own archives, as the
	// air-gap guide says to. RKE2 imports the directory in name order, and
	// under its own name the bundle went first: containerd asked for the
	// pause image to start etcd while RKE2 was still importing the archive
	// that holds it, and tried docker.io for it before the import won.
	for _, host := range caseHosts(c) {
		r.rootOn(host, fmt.Sprintf(`set -e
b=$(ls %s/malmok-images_*linux_amd64.tar.zst 2>/dev/null | head -1)
if [ -z "$b" ]; then
  echo "no platform image bundle in %s."
  echo "Build one with MALMOK_IMAGE_ARCHES=amd64 scripts/airgap-images.sh <tag>"
  echo "and stage it there, beside the RKE2 artifacts."
  exit 1
fi
install -d -m 0755 /var/lib/rancher/rke2/agent/images
cp -f "$b" "/var/lib/rancher/rke2/agent/images/zz-$(basename "$b")"
echo "staged $(basename "$b") as zz-$(basename "$b")"`, c.ArtifactPath, c.ArtifactPath))
	}

	// The charts go beside the document, which is what a staging machine does
	// with them. Downloaded here rather than committed: they are the versions
	// this binary pins, and pinning them twice is how the two drift.
	dir := filepath.Join(r.dir, "charts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	for name, url := range airgapURLs(r.t, r.bin) {
		resp, err := client.Get(url)
		if err != nil || resp.StatusCode != http.StatusOK {
			r.t.Fatalf("fetch %s: %v (status %v)", url, err, resp)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			r.t.Fatalf("read %s: %v", url, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			r.t.Fatal(err)
		}
	}

	for _, host := range caseHosts(c) {
		r.t.Logf("%s: %s", host, strings.ReplaceAll(r.rootOutput(host, airgapOn), "\n", "; "))
	}
	// Restored even when the case fails: a node left cut off makes every
	// later case fail for a reason that has nothing to do with it. What tried
	// to leave is read first, while the rule that logged it still exists.
	return func() {
		for _, host := range caseHosts(c) {
			r.recordDrops(host)
			r.rootOn(host, airgapOff)
		}
	}
}

// recordDrops keeps what each node tried to reach while it was cut off.
//
// "Nothing got out" is what the drop rule guarantees; what an air-gapped site
// needs to know is what tried, because each of those is a firewall ticket or a
// defect. The first logged run found two: a pull of the pause image racing
// its own import, and the lab's NTP pool. Written to the run directory beside
// the logs and summarised in the test output, and recorded whether the case
// passed or not -- a failure is when it matters most.
func (r *labRun) recordDrops(host string) {
	r.t.Helper()
	out, err := r.rootRun(host, airgapDrops, 2*time.Minute)
	path := filepath.Join(r.dir, "airgap-drops-"+host+".txt")
	if werr := os.WriteFile(path, []byte(out+"\n"), 0o644); werr != nil {
		r.t.Errorf("write %s: %v", path, werr)
	}
	if err != nil {
		r.t.Errorf("reading what %s tried to reach: %v\n%s", host, err, out)
		return
	}
	r.t.Logf("%s, dropped while cut off (%s):\n%s", host, path, out)
}

// rootOutput is rootOn for a script whose output is worth keeping.
func (r *labRun) rootOutput(host, script string) string {
	r.t.Helper()
	out, err := r.rootRun(host, script, 2*time.Minute)
	if err != nil {
		r.t.Fatalf("%v\n%s", err, out)
	}
	return out
}

// airgapOn cuts the node off, pods included.
//
// It used to be an iptables chain jumped to from OUTPUT and FORWARD. That cut
// the node's own traffic and let every pod's through: Cilium, starting after
// the rule was placed, inserts its own FORWARD rules ahead of it, and one of
// them accepts everything leaving a pod interface. The air-gapped cases passed
// with the metrics stack's sync job fetching from GitHub, and the README said
// nothing had been fetched.
//
// A table of its own is not subject to that ordering. In nftables a drop in
// any base chain is final whatever another chain on the same hook accepted,
// so Cilium's accept cannot let a packet past this one. Checked on the lab:
// with Cilium restarted after it, a pod timed out reaching github.com and
// still reached the segment.
//
// The table is inet, so IPv6 is cut as well. The lab nodes have no IPv6 route
// out, but a site's nodes may, and a cut that holds only because of how this
// lab happens to be addressed proves nothing about one that has a route. Link
// local and multicast stay open: neighbour discovery is the segment, not the
// internet.
//
// Every drop is logged with where it was going, rate-limited so a loop cannot
// fill the kernel log; the counter still counts what the limit did not log.
// The time of the cut is kept so recordDrops reads only what happened since.
//
// The rules are loaded at boot as well, by a unit ordered before the network
// comes up. The lifecycle case reboots a server to fail it over, and a table
// held only in memory came back without it: the server would have rejoined
// with egress open, and the case would have reported a closed network that
// had been open for its own failover. The wipe removes the unit, so a run
// that dies with the cut in place does not leave it for the next case.
//
// "fwd" is an nftables keyword and cannot name a chain, which is why the
// chains carry the table's prefix.
const airgapOn = `set -e
nft list table ip malmok_airgap >/dev/null 2>&1 && nft delete table ip malmok_airgap
nft list table inet malmok_airgap >/dev/null 2>&1 && nft delete table inet malmok_airgap
cat > /etc/malmok-airgap.nft <<'N'
table inet malmok_airgap {
	chain malmok_output {
		type filter hook output priority -5; policy accept;
		oifname "lo" accept
		ip daddr { 192.168.88.0/24, 10.42.0.0/16, 10.43.0.0/16, 127.0.0.0/8 } accept
		ip6 daddr { fe80::/10, ff00::/8 } accept
		limit rate 20/second burst 50 packets log prefix "malmok-airgap-out: "
		counter drop
	}
	chain malmok_forward {
		type filter hook forward priority -5; policy accept;
		ip daddr { 192.168.88.0/24, 10.42.0.0/16, 10.43.0.0/16, 127.0.0.0/8 } accept
		ip6 daddr { fe80::/10, ff00::/8 } accept
		limit rate 20/second burst 50 packets log prefix "malmok-airgap-fwd: "
		counter drop
	}
}
N
nft -f /etc/malmok-airgap.nft
cat > /etc/systemd/system/malmok-airgap.service <<'U'
[Unit]
Description=Lab air gap for the Malmok verification harness
DefaultDependencies=no
Before=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/malmok-airgap.nft

[Install]
WantedBy=sysinit.target
U
systemctl daemon-reload
systemctl enable --quiet malmok-airgap.service
nft list table inet malmok_airgap >/dev/null
date +%s > /var/lib/malmok-airgap.since
printf 'egress dropped and logged, IPv4 and IPv6; ipv6 default route: '
ip -6 route show default | grep -q . && echo present || echo none`

// airgapDrops summarises what the node tried to reach since the cut: the
// counters, then each destination by chain, protocol and port, then which
// pods the forwarded drops came from. The kernel log is read rather than
// conntrack because a dropped connection never becomes an entry there.
const airgapDrops = `set -e
since=$(cat /var/lib/malmok-airgap.since 2>/dev/null || true)
[ -n "$since" ] || { echo "no record of when the node was cut off; the cut never ran"; exit 1; }
nft list table inet malmok_airgap >/dev/null 2>&1 || { echo "the drop table is gone, so the node was not cut off at the end"; exit 1; }
echo "dropped since the last boot (output, forward): $(nft list table inet malmok_airgap | grep -oE 'packets [0-9]+' | awk '{print $2}' | paste -sd, -)"
journalctl -k --no-pager -o cat --since "@$since" | grep 'malmok-airgap-' > /tmp/malmok-drops || true
echo "logged: $(wc -l < /tmp/malmok-drops)"
sed -E 's/.*malmok-airgap-(out|fwd): .*SRC=([^ ]+) DST=([^ ]+) .*PROTO=([A-Za-z0-9]+)( .*DPT=([0-9]+))?.*/\1 \3 \4 \6/' /tmp/malmok-drops |
  sort | uniq -c | sort -rn | head -30
grep 'malmok-airgap-fwd' /tmp/malmok-drops | grep -oE 'SRC=[^ ]+' | sort | uniq -c | sort -rn | sed 's/SRC=/pod source /' | head -10
rm -f /tmp/malmok-drops`

// airgapOff removes the table and the unit that reloads it at boot, the
// IPv4-only table an earlier harness made, and the iptables chain one before
// that, if either is still there.
const airgapOff = `
[ ! -f /etc/systemd/system/malmok-airgap.service ] || systemctl disable --now --quiet malmok-airgap.service
rm -f /etc/systemd/system/malmok-airgap.service /etc/malmok-airgap.nft
systemctl daemon-reload
nft list table inet malmok_airgap >/dev/null 2>&1 && nft delete table inet malmok_airgap
nft list table ip malmok_airgap >/dev/null 2>&1 && nft delete table ip malmok_airgap
for chain in OUTPUT FORWARD; do iptables -D "$chain" -j MALMOK_AIRGAP 2>/dev/null || true; done
iptables -F MALMOK_AIRGAP 2>/dev/null || true
iptables -X MALMOK_AIRGAP 2>/dev/null || true
rm -f /var/lib/malmok-airgap.since
for family in inet ip; do
  nft list table $family malmok_airgap >/dev/null 2>&1 && { echo "the $family table is still there"; exit 1; }
done
echo "egress restored"`

// onHost runs a check on a named node, and returns what it printed. onNode is
// the same thing against the server, and predates there being a second
// machine worth asking.
func (r *labRun) onHost(host, script string) string {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runner, err := sshexec.Connect(ctx, sshexec.SSHConfig{
		Host: host, User: user, Password: password, InsecureSkipHostKeyCheck: true,
	})
	if err != nil {
		r.t.Fatalf("connect %s: %v", host, err)
	}
	defer runner.Close()

	res, err := runner.Run(ctx, script)
	if err != nil || !res.OK() {
		r.t.Fatalf("%s (exit %d): %s\n%s\n%s", host, res.ExitCode, script, res.Out(), res.Err())
	}
	return strings.TrimSpace(res.Out())
}

// rootOn runs a script as root, for the things only root can do to a node.
func (r *labRun) rootOn(host, script string) {
	r.t.Helper()
	if out, err := r.rootRun(host, script, 2*time.Minute); err != nil {
		r.t.Fatalf("%v\n%s", err, out)
	}
}

// rootRun runs a script as root and reports what happened instead of failing
// the test. The failover asks questions whose "no" is an answer -- which
// server holds the address -- and rootOn cannot ask those.
func (r *labRun) rootRun(host, script string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	runner, err := sshexec.Connect(ctx, sshexec.SSHConfig{
		Host: host, User: user, Password: password, InsecureSkipHostKeyCheck: true,
	})
	if err != nil {
		return "", fmt.Errorf("connect %s: %w", host, err)
	}
	defer runner.Close()

	root, err := sshexec.Elevate(ctx, runner, password)
	if err != nil {
		return "", fmt.Errorf("elevate on %s: %w", host, err)
	}
	res, err := root.Run(ctx, script)
	out := strings.TrimSpace(res.Out() + "\n" + res.Err())
	if err != nil {
		return out, fmt.Errorf("%s: %w", host, err)
	}
	if !res.OK() {
		return out, fmt.Errorf("%s: exit %d", host, res.ExitCode)
	}
	return out, nil
}

// readFile is os.ReadFile for a log that may not exist yet.
func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// labVersions is what the matrix builds at and upgrades between, and where
// that came from.
//
// The channel server, unless the operator names the versions. It is not
// optional in general -- a pinned suite tests yesterday's install -- but it can
// be down: update.rke2.io answered 404 on every path for hours on 2026-09-30,
// while update.k3s.io answered, and the matrix could not start at all. So
// MALMOK_LAB_STABLE and MALMOK_LAB_LATEST stand in for it, both or neither,
// and MALMOK_LAB_UPGRADE_FROM for the release the upgrade case starts from.
// They are an operator's statement, logged as such, never a guess made here:
// with the server down and them unset the suite stops and says what to set.
func labVersions(t *testing.T) (ch rke2.Channels, older, source string) {
	t.Helper()
	ch = rke2.Channels{Stable: os.Getenv("MALMOK_LAB_STABLE"), Latest: os.Getenv("MALMOK_LAB_LATEST")}
	older = os.Getenv("MALMOK_LAB_UPGRADE_FROM")
	switch {
	case ch.Stable != "" && ch.Latest != "":
		source = "versions from the environment, not the channel server"
	case ch.Stable != "" || ch.Latest != "":
		t.Fatal("set both MALMOK_LAB_STABLE and MALMOK_LAB_LATEST, or neither")
	default:
		got, err := rke2.FetchChannels(context.Background())
		if err != nil || got.Stable == "" {
			t.Fatalf("could not read the RKE2 channel: %v. If update.rke2.io is down, set "+
				"MALMOK_LAB_STABLE, MALMOK_LAB_LATEST and MALMOK_LAB_UPGRADE_FROM to the releases to test", err)
		}
		ch, source = got, "versions from the channel server"
	}
	if older == "" {
		older = previousMinor(t, ch.Stable)
	}
	return ch, older, source
}

// previousMinor asks the channel server for the newest release of the minor
// before the one given.
//
// The upgrade case used to build at stable and move to latest, which meant it
// skipped for as long as those two named the same release -- weeks at a time,
// and every run of the matrix so far. An upgrade from the previous minor is
// both always available and the upgrade an operator actually performs.
//
// It parses the same document rke2.FetchChannels does. The product needs two
// answers from it and this needs a third, and widening a product API for a
// test's benefit is how a test's needs end up in a customer's binary.
func previousMinor(t *testing.T, stable string) string {
	t.Helper()

	major, minor, ok := splitMinor(stable)
	if !ok {
		t.Logf("cannot read a minor out of %q, so the upgrade case has nowhere to start", stable)
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://update.rke2.io/v1-release/channels", nil)
	if err != nil {
		t.Logf("channel request: %v", err)
		return ""
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("channel server: %v", err)
		return ""
	}
	defer res.Body.Close()

	var body struct {
		Data []struct {
			ID     string `json:"id"`
			Latest string `json:"latest"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Logf("channel answer: %v", err)
		return ""
	}

	// The highest minor below stable's, not stable's minus one: a minor with
	// no channel of its own would otherwise silently mean "no upgrade case".
	best, bestMinor := "", -1
	for _, c := range body.Data {
		cMajor, cMinor, ok := splitMinor(c.ID + ".0")
		if !ok || cMajor != major || cMinor >= minor || cMinor <= bestMinor {
			continue
		}
		if !strings.Contains(c.Latest, "+rke2r") {
			continue
		}
		best, bestMinor = c.Latest, cMinor
	}
	return best
}

// splitMinor reads the major and minor out of a v1.36.4+rke2r1 or a v1.36.
func splitMinor(v string) (int, int, bool) {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func orNone(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}

// machines are every node the suite owns: the two it always has, and the third
// when one was named.
func machines() []string {
	m := []string{server, agent}
	if third != "" {
		m = append(m, third)
	}
	return m
}

// notRunnable says why a case cannot run on the machines this suite was given,
// or "" when it can. Asked before the wipe: finding out after it costs a round
// of reboots for a case that was never going to run.
func notRunnable(c matrix.Case) string {
	switch {
	case c.Nodes == 3 && third == "":
		return "set MALMOK_LAB_THIRD to a third machine you are willing to lose: " +
			"high availability is three servers, and the suite has been given two"
	// A case that pins an address needs one that exists on the segment. The
	// matrix defaults to the documentation ranges, which is right for a public
	// repository and cannot be installed against: kube-vip claims the VIP on
	// an interface and nothing is on 192.0.2.0/24.
	case c.VIP && vip == "":
		return "set MALMOK_LAB_VIP to a free address on the nodes' segment; " +
			"kube-vip claims it on an interface, so it cannot be a documentation range"
	case c.Exposure == "lb-pool" && lbPool == "":
		return "set MALMOK_LAB_LB_POOL to a free CIDR on the nodes' segment"
	// The lifecycle upgrades between two carried releases, and the harness
	// cannot fetch either.
	case c.Op == matrix.OpLifecycle && airgapFrom == "":
		return "set MALMOK_LAB_AIRGAP_FROM to the release the lifecycle case builds at, " +
			"staged on every node beside MALMOK_LAB_AIRGAP_VERSION's as <artifact path>-<version>"
	}
	return ""
}

// kubeRoot points kubectl at a server's own kubeconfig. Every server has one;
// only the first is given the operator's.
const kubeRoot = "export PATH=$PATH:/var/lib/rancher/rke2/bin KUBECONFIG=/etc/rancher/rke2/rke2.yaml\n"

// failover reboots the server answering for the VIP and requires the cluster
// to carry on without it.
//
// Three things have to hold for "high availability" to mean anything, and each
// is measured on the machines rather than inferred:
//
//   - The address moves. kube-vip runs on every server and elects one to
//     answer for the VIP; with that one gone another has to claim it, or every
//     kubelet and every client is talking to an address nobody holds.
//   - Writes still land. A read can be served from the API server's cache with
//     etcd in pieces; a write needs two of three members to agree, so a write
//     through the VIP is the observable.
//   - The server comes back. A cluster that survives losing a node and cannot
//     readmit it has only postponed the failure.
//
// The server taken away is the one holding the address, not any server.
// Losing a node nobody was routed to proves only that the cluster can lose a
// node nobody was using.
//
// Taken away by a reboot, not by killing RKE2. Killing it leaves the machine
// up and the VIP on its interface -- kube-vip dies by SIGKILL with no chance to
// release it -- so the lost server went on answering for the address and sent
// its own requests to itself. That is a real hazard, written up in the
// recovery guide where an operator stopping a server will read it, but it is a
// different failure from the one this case is about: a server that goes away.
// A reboot takes the machine, and the address, with it.
func (r *labRun) failover() {
	r.t.Helper()

	servers := machines()
	holder := r.vipHolder(servers)
	if holder == "" {
		r.t.Fatalf("no server answers for the VIP %s, so there is nothing to fail over from", vip)
	}
	var survivors []string
	for _, h := range servers {
		if h != holder {
			survivors = append(survivors, h)
		}
	}
	r.t.Logf("%s holds the VIP; rebooting it", holder)

	// The error is ignored for the reason wipe ignores it: the connection is
	// going down with the machine, and the boot id is what says it rebooted.
	before := r.bootID(holder)
	_, _ = r.rootRun(holder, `nohup sh -c 'sleep 1; reboot' >/dev/null 2>&1 & echo rebooting`, 30*time.Second)

	moved := ""
	for deadline := time.Now().Add(2 * time.Minute); moved == "" && time.Now().Before(deadline); {
		if moved = r.vipHolder(survivors); moved == "" {
			time.Sleep(3 * time.Second)
		}
	}
	if moved == "" {
		out, _ := r.rootRun(survivors[0], kubeRoot+
			`kubectl -n kube-system get pods -l app.kubernetes.io/name=kube-vip -o wide 2>&1; `+
			`kubectl -n kube-system logs -l app.kubernetes.io/name=kube-vip --tail=20 2>&1`, time.Minute)
		r.t.Fatalf("the VIP %s did not move off %s within 2m:\n%s", vip, holder, out)
	}
	r.t.Logf("the VIP moved to %s", moved)

	// The loss has to be real when the write lands. A server already back on
	// a new boot proves nothing about the others working without it. Still on
	// the old boot is a machine mid-shutdown, whose control plane has already
	// stopped, and counts.
	if id := r.bootID(holder); id != "" && id != before {
		r.t.Fatalf("%s was back (boot %s) before the cluster had to work without it, "+
			"so the check would prove nothing", holder, id)
	}

	// A write, through the address, from a server that was not lost.
	r.t.Log(r.writeThroughVIP(moved, holder+" down"))

	// And the lost server comes back by itself. Nothing here starts it: a
	// server that needs somebody to log in after a power cut is not highly
	// available, and until this case nothing had checked that RKE2 starts on
	// boot rather than on install.
	for deadline := time.Now().Add(5 * time.Minute); ; {
		if id := r.bootID(holder); id != "" && id != before {
			break
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("%s did not come back from the reboot within 5m", holder)
		}
		time.Sleep(5 * time.Second)
	}
	out, err := r.rootRun(moved, kubeRoot+fmt.Sprintf(`k="kubectl --server https://%s:6443 --request-timeout=10s"
for i in $(seq 1 60); do
  n=$($k get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l)
  [ "$n" -ge %d ] && { echo "all %d servers are Ready again"; exit 0; }
  sleep 10
done
$k get nodes 2>&1
exit 1`, vip, len(servers), len(servers)), 12*time.Minute)
	if err != nil {
		r.t.Fatalf("%s did not rejoin within 10m: %v\n%s", holder, err, out)
	}
	r.t.Log(out)
}

// writeThroughVIP makes a write through the VIP from one server, retrying for
// two minutes, and fails the test if none lands.
//
// A read can be served from the API server's cache with etcd in pieces; a
// write needs a quorum to agree, so a write is the observable. The kubeconfig
// on every server points at its own API server; --server sends the request to
// the VIP instead, which is the address kubelets and clients are given -- and
// whose certificate has to name it.
func (r *labRun) writeThroughVIP(from, while string) string {
	r.t.Helper()
	probe := fmt.Sprintf("malmok-write-%d", time.Now().UnixNano())
	out, err := r.rootRun(from, kubeRoot+fmt.Sprintf(`k="kubectl --server https://%s:6443 --request-timeout=10s"
out=""
for i in $(seq 1 24); do
  if out=$(printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  namespace: default\ndata:\n  at: "%%s"\n' "$(date +%%s)" | $k apply -f - 2>&1); then
    $k -n default delete configmap %s --ignore-not-found >/dev/null 2>&1
    echo "a write through the VIP landed with %s"
    exit 0
  fi
  sleep 5
done
echo "no write through the VIP landed within 2m. The last answer was:"
echo "$out"
exit 1`, vip, probe, probe, while), 4*time.Minute)
	if err != nil {
		r.t.Fatalf("%v\n%s", err, out)
	}
	return out
}

// caseHosts are the machines a case is built on: the server and the agent,
// and the third machine when the case is three servers.
func caseHosts(c matrix.Case) []string {
	if c.Nodes == 3 {
		return []string{server, agent, third}
	}
	return []string{server, agent}
}

// stageRelease makes a node's artifact path hold the given release.
//
// Every node carries two releases, the one in the path and the other beside
// it as <path>-<version>, the version with or without its +rke2r build suffix;
// this swaps the two directories by name when the path holds the wrong one. Which release a directory holds is read from the rke2
// binary inside its tarball, because the archive names carry no version. A
// tarball an interrupted run hid is put back first, with a word about it.
func (r *labRun) stageRelease(host, path, want string) string {
	r.t.Helper()
	return r.onHost(host, fmt.Sprintf(`d=%s; want=%s
t=rke2.linux-amd64.tar.gz
other="$d-$want"; [ -d "$other" ] || other="$d-${want%%%%+*}"
for x in "$d" "$other"; do
  [ -f "$x/$t.lost" ] && [ ! -e "$x/$t" ] && mv "$x/$t.lost" "$x/$t" && echo "put back $x/$t, hidden by an earlier run;"
done
ver() { b=$(mktemp); tar -xzf "$1/$t" -O bin/rke2 > "$b" 2>/dev/null && chmod +x "$b" && "$b" --version 2>/dev/null | awk 'NR==1{print $3}'; rm -f "$b"; }
[ -d "$d" ] || { echo "no $d"; exit 1; }
cur=$(ver "$d")
[ -n "$cur" ] || { echo "cannot read which release $d holds"; exit 1; }
[ "$cur" = "$want" ] && { echo "$d holds $want"; exit 0; }
[ -d "$other" ] || { echo "$d holds $cur, and there is no $d-$want or $d-${want%%%%+*} to swap in"; exit 1; }
[ ! -e "$d-$cur" ] || { echo "$d holds $cur and $d-$cur is already there"; exit 1; }
mv "$d" "$d-$cur" && mv "$other" "$d"
echo "$d now holds $want; $cur moved to $d-$cur"`, path, want))
}

// expectSchedulable fails when any node still refuses work: an upgrade that
// ends with a node cordoned has not finished, whatever it printed.
func (r *labRun) expectSchedulable() {
	r.t.Helper()
	r.onNode(`c=$(kubectl get nodes --no-headers -o custom-columns=N:.metadata.name,U:.spec.unschedulable | awk '$2=="true"'); ` +
		`[ -z "$c" ] || { echo "still cordoned: $c"; exit 1; }`)
}

// upgradeLosingArtifacts takes the three servers through an air-gapped
// upgrade the way a site will meet one: refused while the old release is
// still staged, then started with the new one and losing the second server's
// tarball as that server's drain begins, then finished.
//
// The loss is real, not simulated: the tarball is moved away on the node, so
// the install step meets exactly what a half-copied directory gives it. What
// has to hold is that the run stops on that server and says so, the other two
// keep the quorum -- a write through the VIP lands -- and a second run, once
// the tarball is back, finishes the job and leaves no node cordoned.
func (r *labRun) upgradeLosingArtifacts(c matrix.Case, doc string) {
	r.t.Helper()
	hosts := caseHosts(c)
	bundle := filepath.Join(r.dir, "out")

	out, err := r.malmok(10*time.Minute, "upgrade", "-f", doc, "--to", airgapVersion,
		"--insecure-host-key", "--approve", "--timeout", "10m", "--bundle", bundle)
	if err == nil || !strings.Contains(out, "UP-006") || !strings.Contains(out, "nothing has been changed") {
		r.t.Fatalf("an upgrade with %s still staged was not refused by UP-006 (err %v):\n%s",
			airgapFrom, err, tail(out, 30))
	}
	r.expectVersion(airgapFrom)
	r.expectSchedulable()
	r.t.Log("refused by UP-006 with the old release staged, and nothing changed")

	for _, host := range hosts {
		r.t.Logf("%s: %s", host, r.stageRelease(host, c.ArtifactPath, airgapVersion))
	}

	second := hosts[1]
	tarball := c.ArtifactPath + "/rke2.linux-amd64.tar.gz"
	log := filepath.Join(r.dir, "upgrade-lost.log")
	f, err := os.Create(log)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command(r.bin, "upgrade", "-f", doc, "--to", airgapVersion,
		"--insecure-host-key", "--approve", "--timeout", "45m", "--bundle", bundle)
	cmd.Env = append(os.Environ(), "NODE_PASSWORD="+password)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	marker := "upgrade-server/drain/" + second
	hidden := false
	var runErr error
	deadline := time.After(50 * time.Minute)
	for finished := false; !finished; {
		select {
		case runErr = <-done:
			finished = true
		case <-time.After(2 * time.Second):
			if !hidden && strings.Contains(readFile(log), marker) {
				r.onHost(second, fmt.Sprintf(`mv %s %s.lost`, tarball, tarball))
				r.t.Logf("%s: the tarball went missing as its drain began", second)
				hidden = true
			}
		case <-deadline:
			_ = cmd.Process.Kill()
			r.t.Fatalf("the upgrade neither finished nor failed within 50m:\n%s", tail(readFile(log), 30))
		}
	}
	body := readFile(log)
	switch {
	case !hidden:
		r.t.Fatalf("the upgrade ended (%v) before %s began, so nothing was lost:\n%s",
			runErr, marker, tail(body, 30))
	case runErr == nil:
		r.t.Fatalf("the upgrade succeeded with %s's tarball gone:\n%s", second, tail(body, 30))
	case !strings.Contains(body, "upgrade-server/install/"+second):
		r.t.Fatalf("the upgrade failed without reaching %s's install, so the loss is not what "+
			"stopped it:\n%s", second, tail(body, 30))
	}
	r.t.Logf("the upgrade stopped on %s:\n%s", second, tail(body, 12))

	// The quorum held with one server new, one stopped mid-upgrade and one
	// not yet reached, and the run said where it stopped.
	r.t.Log(r.writeThroughVIP(hosts[0], second+" stopped mid-upgrade"))
	left, err := r.rootRun(hosts[0], kubeRoot+fmt.Sprintf(`kubectl get nodes --no-headers -o custom-columns=N:.metadata.name,R:.status.conditions[-1].status,U:.spec.unschedulable,V:.status.nodeInfo.kubeletVersion
n=$(kubectl get nodes -l node-role.kubernetes.io/etcd=true --no-headers | awk '$2 ~ /^Ready/' | wc -l)
[ "$n" = %d ] || { echo "only $n etcd members Ready"; exit 1; }`, len(hosts)), time.Minute)
	if err != nil {
		r.t.Fatalf("after the failed upgrade: %v\n%s", err, left)
	}
	r.t.Logf("left behind:\n%s", left)

	r.onHost(second, fmt.Sprintf(`mv %s.lost %s`, tarball, tarball))
	r.upgrade(doc, airgapVersion)
	r.expectVersion(airgapVersion)
	r.expectSchedulable()
	r.t.Logf("finished once the tarball was back: every server on %s, none cordoned", airgapVersion)

	// The document now describes the cluster that exists, which is what the
	// restore and the certificate scan after it read.
	r.airgapAt = airgapVersion
	r.write(c, false)
}

// restoreAll restores the three servers from one etcd snapshot, the way RKE2's
// documentation does it for a cluster with more than one server: every server
// stopped, the first reset from the snapshot and started, and the others'
// databases removed so they rejoin the restored member rather than outvote it.
//
// Marked on both sides of the snapshot, so the result is read off the cluster:
// what was made before it is there, what was made after it is not, and three
// etcd members are Ready again.
func (r *labRun) restoreAll(c matrix.Case) {
	r.t.Helper()
	hosts := caseHosts(c)
	first, rest := hosts[0], hosts[1:]
	stamp := time.Now().Unix()
	before := fmt.Sprintf("malmok-restore-before-%d", stamp)
	after := fmt.Sprintf("malmok-restore-after-%d", stamp)
	cm := func(name string) string {
		return fmt.Sprintf(`kubectl -n default create configmap %s --from-literal=at=%d`, name, stamp)
	}

	r.onNode(cm(before))
	snap := r.rootOutput(first, `set -e
n=malmok-lifecycle-$(date +%s)
out=$(rke2 etcd-snapshot save --name "$n" 2>&1) || { echo "$out"; exit 1; }
ls -t /var/lib/rancher/rke2/server/db/snapshots/"$n"* | head -1`)
	r.t.Logf("snapshot %s", snap)
	r.onNode(cm(after))

	for _, h := range append(rest, first) {
		r.rootOn(h, "systemctl stop rke2-server")
	}
	out, err := r.rootRun(first, fmt.Sprintf(`log=/tmp/malmok-cluster-reset.log
timeout 900 rke2 server --cluster-reset --cluster-reset-restore-path=%s > "$log" 2>&1
rc=$?
tail -3 "$log" | cut -c1-200
grep -q "restart without --cluster-reset" "$log" || { echo "the reset did not finish (exit $rc)"; exit 1; }`, snap), 16*time.Minute)
	if err != nil {
		r.t.Fatalf("cluster-reset on %s: %v\n%s", first, err, out)
	}
	// The reset exits once the membership is rewritten; the server then has
	// to be started as it normally is, without the flag.
	if out, err := r.rootRun(first, `systemctl start rke2-server`, 12*time.Minute); err != nil {
		r.t.Fatalf("start %s after the reset: %v\n%s", first, err, out)
	}
	r.waitServerReady(first, 1)
	for _, h := range rest {
		if out, err := r.rootRun(h, `rm -rf /var/lib/rancher/rke2/server/db && systemctl start rke2-server`, 12*time.Minute); err != nil {
			r.t.Fatalf("rejoin %s: %v\n%s", h, err, out)
		}
	}
	r.waitServerReady(first, len(hosts))

	r.onNode(fmt.Sprintf(`kubectl -n default get configmap %s >/dev/null || { echo "%s, made before the snapshot, is gone"; exit 1; }
if kubectl -n default get configmap %s >/dev/null 2>&1; then echo "%s, made after the snapshot, survived the restore"; exit 1; fi
kubectl -n default delete configmap %s >/dev/null`, before, before, after, after, before))
	r.t.Logf("restored from %s: the marker made before it is back, the one made after it is gone, "+
		"%d etcd members Ready", snap, len(hosts))
}

// waitServerReady waits, asking the given server's own API server, until at
// least n etcd members report Ready. Asked locally because the VIP is held by
// kube-vip, which runs on the very servers being brought back.
func (r *labRun) waitServerReady(host string, n int) {
	r.t.Helper()
	out, err := r.rootRun(host, kubeRoot+fmt.Sprintf(`for i in $(seq 1 90); do
  got=$(kubectl get nodes -l node-role.kubernetes.io/etcd=true --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l)
  [ "$got" -ge %d ] && { echo "$got etcd members Ready"; exit 0; }
  sleep 10
done
kubectl get nodes 2>&1
exit 1`, n), 17*time.Minute)
	if err != nil {
		r.t.Fatalf("waiting for %d etcd members on %s: %v\n%s", n, host, err, out)
	}
	r.t.Log(out)
}

// vipHolder is the server whose interface carries the VIP, or "" when none of
// them does.
func (r *labRun) vipHolder(hosts []string) string {
	for _, h := range hosts {
		if _, err := r.rootRun(h, fmt.Sprintf(`ip -4 -o addr show | grep -qwF %s`, vip), 20*time.Second); err == nil {
			return h
		}
	}
	return ""
}
