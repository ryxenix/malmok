// Lab harness: wipe the throwaway nodes, and read a built cluster back the
// way its operator would.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"platform.ryxen.dev/malmok/internal/exec"
)

func root(ctx context.Context, host, pw string) exec.Runner {
	r, err := exec.Connect(ctx, exec.SSHConfig{Host: host, User: "k8s", Password: pw, InsecureSkipHostKeyCheck: true})
	if err != nil {
		fmt.Println(host, "connect:", err)
		os.Exit(1)
	}
	e, err := exec.Elevate(ctx, r, pw)
	if err != nil {
		fmt.Println(host, "elevate:", err)
		os.Exit(1)
	}
	return e
}

func wipe(hosts []string, pw string) {
	for _, h := range hosts {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		e := root(ctx, h, pw)
		res, _ := e.Run(ctx, `
[ -x /usr/local/bin/rke2-killall.sh ] && /usr/local/bin/rke2-killall.sh >/dev/null 2>&1 || true
[ -x /usr/local/bin/rke2-uninstall.sh ] && /usr/local/bin/rke2-uninstall.sh >/dev/null 2>&1 || true
rm -f /usr/local/bin/kubectl /usr/local/bin/k9s
rm -rf /etc/rancher /var/lib/rancher /var/lib/kubelet /etc/cni /opt/cni /var/lib/cni
rm -f /root/.kube/config /home/k8s/.kube/config
echo wiped`)
		fmt.Printf("%s %s", h, res.Stdout)
		// The reboot is not decoration: rke2-uninstall leaves Cilium's tc/eBPF
		// programs attached to the NIC, and they drop peer traffic next build.
		_, _ = e.Run(ctx, `nohup sh -c 'sleep 1; reboot' >/dev/null 2>&1 &`)
		fmt.Println(h, "rebooting")
		cancel()
	}
}

func verify(host, pw string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := exec.Connect(ctx, exec.SSHConfig{Host: host, User: "k8s", Password: pw, InsecureSkipHostKeyCheck: true})
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(1)
	}
	res, _ := r.Run(ctx, `
echo "## nodes";       kubectl get nodes --no-headers 2>&1 | awk '{print $1, $2, $5}'
echo "## not running"; kubectl get pods -A --no-headers 2>&1 | awk '$4!="Running" && $4!="Completed" {print $1, $2, $4}' | head -8
echo "## gateway";     kubectl get gateway -A --no-headers 2>&1 | awk '{print $2, $4, $5}'
echo "## listeners";   kubectl get gateway -A -o jsonpath='{range .items[*].status.listeners[*]}{.name}{"="}{.attachedRoutes}{" "}{end}{"\n"}' 2>&1
echo "## certs";       kubectl get secret -A --no-headers 2>&1 | grep -iE 'tls|cert' | awk '{print $1, $2, $3}' | head -5
echo "## tools";       command -v kubectl >/dev/null && echo kubectl-ok; command -v k9s >/dev/null && echo k9s-ok`)
	fmt.Println(strings.TrimSpace(res.Stdout))
}

func main() {
	pw := os.Getenv("NODE_PASSWORD")
	if len(os.Args) < 3 {
		fmt.Println("usage: zzlab <wipe|verify> <host>...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "wipe":
		wipe(os.Args[2:], pw)
	case "verify":
		verify(os.Args[2], pw)
	}
}
