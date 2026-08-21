package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"platform.ryxen.dev/malmok/internal/exec"
)

func main() {
	pw := os.Getenv("NODE_PASSWORD")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, _ := exec.Connect(ctx, exec.SSHConfig{Host: "192.168.88.241", User: "k8s", Password: pw, InsecureSkipHostKeyCheck: true})
	root, _ := exec.Elevate(ctx, r, pw)
	res, _ := root.Run(ctx, `export PATH=$PATH:/var/lib/rancher/rke2/bin; export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
echo "## gatewayclass"; kubectl get gatewayclass 2>&1 | head -3
echo "## operator pods"; kubectl -n kube-system get pods -l io.cilium/app=operator -o wide --no-headers 2>&1 | awk '{print $1, $3, $5}'
echo "## helm job"; kubectl -n kube-system get job helm-install-rke2-cilium --no-headers 2>&1 | head -2
echo "## operator gateway lines"; kubectl -n kube-system logs -l io.cilium/app=operator --tail=100 2>&1 | grep -iE 'gateway' | tail -4`)
	fmt.Println(res.Stdout, res.Stderr)
}
