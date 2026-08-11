package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"platform.ryxen.dev/platformctl/internal/exec"
)

func main() {
	b, _ := os.ReadFile(os.Args[4])
	r, err := exec.Dial(context.Background(), exec.SSHConfig{
		Host: os.Args[1], User: os.Args[2], Password: os.Args[3],
		InsecureSkipHostKeyCheck: true, Timeout: 10 * time.Second,
	})
	if err != nil {
		panic(err)
	}
	defer r.Close()
	to := 120 * time.Second
	if len(os.Args) > 5 {
		to, _ = time.ParseDuration(os.Args[5])
	}
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	res, _ := exec.Sudo{Runner: r, Password: os.Args[3]}.Run(ctx, string(b))
	fmt.Print(res.Stdout)
	if s := res.Err(); s != "" {
		fmt.Println("[stderr]", s)
	}
}
