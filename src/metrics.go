package main

import (
	"io"
	"sync/atomic"
)

type readerMetrics struct {
	io.ReadWriteCloser
	nRead    atomic.Int64
	nWritten atomic.Int64
}

func newReaderMetrics(rw io.ReadWriteCloser) *readerMetrics {
	return &readerMetrics{ReadWriteCloser: rw}
}

func (s *readerMetrics) BytesRead() int    { return int(s.nRead.Load()) }
func (s *readerMetrics) BytesWritten() int { return int(s.nWritten.Load()) }

func (s *readerMetrics) Read(p []byte) (n int, err error) {
	n, err = s.ReadWriteCloser.Read(p)
	if n > 0 {
		s.nRead.Add(int64(n))
	}
	return
}

func (s *readerMetrics) Write(p []byte) (n int, err error) {
	n, err = s.ReadWriteCloser.Write(p)
	if n > 0 {
		s.nWritten.Add(int64(n))
	}
	return
}

func (s *readerMetrics) Close() error { return s.ReadWriteCloser.Close() }
