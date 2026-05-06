package app

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestGracefulShutdownOnSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process SIGTERM delivery is not supported consistently on Windows")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find process: %v", err)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = proc.Signal(syscall.SIGTERM)
	}()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context was not canceled by SIGTERM")
	}
}
