package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestInterruptibleStdoutFlushesBeforeFinish(t *testing.T) {
	destination := &syncBuffer{}
	output, finish := interruptibleStdout(context.Background(), destination)
	if _, err := output.Write([]byte("archive bytes")); err != nil {
		t.Fatal(err)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if destination.String() != "archive bytes" {
		t.Fatalf("destination = %q", destination.String())
	}
}

func TestInterruptibleStdoutPreservesDestinationErrors(t *testing.T) {
	failure := errors.New("the destination failed")
	output, finish := interruptibleStdout(context.Background(), writerFunc(func([]byte) (int, error) {
		return 0, failure
	}))
	_, _ = output.Write([]byte("archive bytes"))
	if err := finish(); !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanceledInterruptibleStdoutDoesNotWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wrote := false
	output, finish := interruptibleStdout(ctx, writerFunc(func([]byte) (int, error) {
		wrote = true
		return 0, nil
	}))
	if _, err := output.Write([]byte("archive bytes")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := finish(); !errors.Is(err, context.Canceled) {
		t.Fatalf("finish err = %v", err)
	}
	if wrote {
		t.Fatal("a canceled output reached the destination")
	}
}

func TestInterruptibleStdoutDoesNotWaitForABlockedDestinationOnCancel(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	destination := writerFunc(func(p []byte) (int, error) {
		entered <- struct{}{}
		<-release
		return len(p), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	output, finish := interruptibleStdout(ctx, destination)
	writeResult := make(chan error, 1)
	go func() {
		_, err := output.Write([]byte("blocked output"))
		writeResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the destination was never reached")
	}
	cancel()
	select {
	case <-writeResult:
	case <-time.After(2 * time.Second):
		t.Fatal("the write did not stop after cancellation")
	}
	finished := make(chan error, 1)
	go func() { finished <- finish() }()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("finish err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finish waited for a destination that was still blocked")
	}
	close(release)
}

func TestInterruptibleStdoutCanCancelDuringFinalFlush(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output, finish := interruptibleStdout(ctx, writerFunc(func(p []byte) (int, error) {
		close(entered)
		<-release
		return len(p), nil
	}))
	if _, err := output.Write([]byte("the final archive bytes")); err != nil {
		t.Fatal(err)
	}
	<-entered
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		finished <- finish()
	}()
	<-started
	select {
	case err := <-finished:
		t.Fatalf("finish returned before the blocked final bytes were written: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("final flush cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop the final output flush")
	}
}
