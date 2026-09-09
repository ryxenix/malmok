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

// The lab it runs against. Addresses come from the environment so the suite
// can be pointed at a different pair without an edit.
var (
	server   = env("MALMOK_LAB_SERVER", "192.168.88.241")
	agent    = env("MALMOK_LAB_AGENT", "192.168.88.244")
	user     = env("MALMOK_LAB_USER", "k8s")
	password = os.Getenv("NODE_PASSWORD")
	binary   = env("MALMOK_BIN", "../../bin/malmok")

	// airgapVersion is the RKE2 release the artifacts staged on the nodes
	// carry. It cannot be read from them -- the archive names hold no version
	// -- and it cannot be the channel's newest, because the artifacts are
	// 1.3GB and are staged out of band rather than downloaded per run. Unset,
	// the air-gapped case skips and says what to stage.
	airgapVersion = os.Getenv("MALMOK_LAB_AIRGAP_VERSION")

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
	// every case with a VIP failed at vip-interface saying precisely that. The
	// matrix must not name a real network -- it is a public default aimed at
	// whatever answers there -- so the harness supplies them instead.
	//
	// Both must be free on the segment and outside whatever hands out leases.
	vip    = env("MALMOK_LAB_VIP", "192.168.88.210")
	lbPool = env("MALMOK_LAB_LB_POOL", "192.168.88.216/29")
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
	older := previousMinor(t, ch.Stable)
	t.Logf("matrix on %s: %d cases, RKE2 stable %s, latest %s, upgrades from %s",
		server, len(matrix.Cases()), ch.Stable, ch.Latest, orNone(older))

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
}

// execute builds the case and then does to it whatever its operation says.
func (r *labRun) execute(c matrix.Case) {
	if c.Network == "airgap" {
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
		version = airgapVersion
	}
	doc := c.Document(version, matrix.Hosts{
		Server: server, Agent: agent, User: user, PasswordRef: "env://NODE_PASSWORD",
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

	// Read before rebooting: after, there is nothing to compare against.
	before := map[string]string{}
	for _, host := range []string{server, agent} {
		before[host] = r.bootID(host)
	}

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
	r.waitForNodes(before)
}

const wipeScript = `
[ -x /usr/local/bin/rke2-killall.sh ] && /usr/local/bin/rke2-killall.sh >/dev/null 2>&1 || true
[ -x /usr/local/bin/rke2-uninstall.sh ] && /usr/local/bin/rke2-uninstall.sh >/dev/null 2>&1 || true
rm -f /usr/local/bin/kubectl /usr/local/bin/k9s
rm -rf /etc/rancher /var/lib/rancher /var/lib/kubelet /etc/cni /opt/cni /var/lib/cni
rm -f /root/.kube/config /home/*/.kube/config
echo wiped`

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
	for _, host := range []string{server, agent} {
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
// The nodes are cut off from everything but the segment with DROP rather than
// REJECT. The difference is not cosmetic: a rejected connection fails at once
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

	// The artifacts are staged out of band -- they are 1.3GB and survive a
	// wipe -- so this asks rather than copies, and says exactly what is
	// missing rather than failing later inside the install.
	for _, host := range []string{server, agent} {
		r.onHost(host, fmt.Sprintf(
			`d=%s
[ -d "$d" ] || { echo "no artifact directory at $d; stage the RKE2 release into it"; exit 1; }
for f in rke2.linux-amd64.tar.gz sha256sum-amd64.txt install.sh \
         rke2-images.linux-amd64.tar.zst rke2-images-cilium.linux-amd64.tar.zst \
         gateway-api-v1.4.1-standard-install.yaml; do
  [ -f "$d/$f" ] || { echo "$d is missing $f"; exit 1; }
done`, c.ArtifactPath))
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

	for _, host := range []string{server, agent} {
		r.rootOn(host, airgapOn)
	}
	// Restored even when the case fails: a node left cut off makes every
	// later case fail for a reason that has nothing to do with it.
	return func() {
		for _, host := range []string{server, agent} {
			r.rootOn(host, airgapOff)
		}
	}
}

const airgapOn = `
for chain in OUTPUT FORWARD; do iptables -D "$chain" -j MALMOK_AIRGAP 2>/dev/null || true; done
iptables -F MALMOK_AIRGAP 2>/dev/null || iptables -N MALMOK_AIRGAP
iptables -A MALMOK_AIRGAP -o lo -j RETURN
iptables -A MALMOK_AIRGAP -d 192.168.88.0/24 -j RETURN
iptables -A MALMOK_AIRGAP -d 10.42.0.0/16 -j RETURN
iptables -A MALMOK_AIRGAP -d 10.43.0.0/16 -j RETURN
iptables -A MALMOK_AIRGAP -d 127.0.0.0/8 -j RETURN
iptables -A MALMOK_AIRGAP -j DROP
iptables -I OUTPUT 1 -j MALMOK_AIRGAP
iptables -I FORWARD 1 -j MALMOK_AIRGAP
echo "egress dropped"`

const airgapOff = `
for chain in OUTPUT FORWARD; do iptables -D "$chain" -j MALMOK_AIRGAP 2>/dev/null || true; done
iptables -F MALMOK_AIRGAP 2>/dev/null || true
iptables -X MALMOK_AIRGAP 2>/dev/null || true
echo "egress restored"`

// onHost runs a check on a named node. onNode is the same thing against the
// server, and predates there being a second machine worth asking.
func (r *labRun) onHost(host, script string) {
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
}

// rootOn runs a script as root, for the things only root can do to a node.
func (r *labRun) rootOn(host, script string) {
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

	root, err := sshexec.Elevate(ctx, runner, password)
	if err != nil {
		r.t.Fatalf("elevate on %s: %v", host, err)
	}
	if res, err := root.Run(ctx, script); err != nil || !res.OK() {
		r.t.Fatalf("%s (exit %d): %s\n%s", host, res.ExitCode, res.Out(), res.Err())
	}
}

// readFile is os.ReadFile for a log that may not exist yet.
func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
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
