// Step the lab clocks rather than waiting for a slew: timesyncd corrects a
// large offset by slewing, which takes an hour for 1.8s.
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
	for _, h := range []string{"192.168.88.241", "192.168.88.244"} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		r, _ := exec.Connect(ctx, exec.SSHConfig{Host: h, User: "k8s", Password: pw, InsecureSkipHostKeyCheck: true})
		root, err := exec.Elevate(ctx, r, pw)
		if err != nil {
			fmt.Println(h, err)
			os.Exit(1)
		}
		// Stop the daemon, step the clock to this machine's (NTP-synced) idea
		// of now, then let the daemon take over again.
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		res, _ := root.Run(ctx, fmt.Sprintf(`
timedatectl set-ntp false
sleep 1
timedatectl set-time '%s'
timedatectl set-ntp true
systemctl restart systemd-timesyncd
sleep 3
date -u +%%s.%%3N`, now))
		fmt.Printf("%s -> %s %s\n", h, res.Stdout, res.Stderr)
		cancel()
	}
}
