package main

import (
	"context"
	"io"
	"sync"
)

type commandStdin struct {
	ctx     context.Context
	source  io.Reader
	reader  *io.PipeReader
	writer  *io.PipeWriter
	started sync.Once
}

func interruptibleStdin(ctx context.Context, source io.Reader) (io.Reader, func()) {
	reader, writer := io.Pipe()
	input := &commandStdin{ctx: ctx, source: source, reader: reader, writer: writer}
	stop := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
	return input, func() {
		stop()
		_ = reader.Close()
		_ = writer.Close()
	}
}

func (s *commandStdin) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	s.started.Do(func() {
		go func() {
			_, err := io.Copy(s.writer, s.source)
			_ = s.writer.CloseWithError(err)
		}()
	})
	return s.reader.Read(p)
}
