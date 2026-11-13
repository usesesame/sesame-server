package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type untouchedStdin struct {
	t *testing.T
}

func (s untouchedStdin) Read([]byte) (int, error) {
	s.t.Error("the command read standard input before requesting it")
	return 0, io.EOF
}

func TestClosingUnusedStandardInputDoesNotReadIt(t *testing.T) {
	_, closeInput := interruptibleStdin(context.Background(), untouchedStdin{t: t})
	closeInput()
}

func TestCanceledStandardInputDoesNotReadItsSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input, closeInput := interruptibleStdin(ctx, untouchedStdin{t: t})
	defer closeInput()
	cancel()
	if _, err := input.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input error: %v", err)
	}
}

func TestStandardInputPreservesSourceErrors(t *testing.T) {
	failure := errors.New("input failed")
	source := io.MultiReader(strings.NewReader("partial archive"), failingStdin{err: failure})
	input, closeInput := interruptibleStdin(context.Background(), source)
	defer closeInput()
	data, err := io.ReadAll(input)
	if string(data) != "partial archive" || !errors.Is(err, failure) {
		t.Fatalf("input result: %q, %v", data, err)
	}
}

type failingStdin struct {
	err error
}

func (s failingStdin) Read([]byte) (int, error) { return 0, s.err }
