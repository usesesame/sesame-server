package main

import (
	"context"
	"io"
)

type commandStdout struct {
	ctx    context.Context
	reader *io.PipeReader
	writer *io.PipeWriter
	done   chan error
	stop   func() bool
}

func interruptibleStdout(ctx context.Context, destination io.Writer) (io.Writer, func() error) {
	reader, writer := io.Pipe()
	output := &commandStdout{ctx: ctx, reader: reader, writer: writer, done: make(chan error, 1)}
	output.stop = context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
	go func() {
		_, err := io.Copy(destination, reader)
		_ = reader.CloseWithError(err)
		output.done <- err
	}()
	return output, output.finish
}

func (s *commandStdout) Write(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	return s.writer.Write(p)
}

func (s *commandStdout) finish() error {
	defer s.stop()
	_ = s.writer.Close()
	select {
	case err := <-s.done:
		if err != nil {
			return err
		}
		return s.ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
