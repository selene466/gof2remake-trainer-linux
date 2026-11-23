package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gof2remake-trainer/internal/trainer"
	"gof2remake-trainer/internal/ui"
)

// Usage: gof2remake-trainer /path/GoF2Remake.x86_64 [args]
// The game is started as our child so /proc/PID/mem is writable under ptrace_scope=1.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gof2remake-trainer GAME_COMMAND [ARGS]")
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "trainer: cannot start game:", err)
		os.Exit(1)
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()

	tr := trainer.New()
	go tr.Run()
	go tr.ServeDebug()
	go ui.Run(tr, filepath.Join(filepath.Dir(os.Args[1]), "GoF2Remake_Data"))

	err := cmd.Wait()
	tr.Stop()
	time.Sleep(100 * time.Millisecond)
	if ee, ok := err.(*exec.ExitError); ok {
		os.Exit(ee.ExitCode())
	}
}
