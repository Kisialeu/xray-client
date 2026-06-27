package main

import (
	"errors"
	"io"
	"testing"
)

func TestReaderMetrics_ReadWrite(t *testing.T) {
	buf := &fakeBuf{}
	rm := newReaderMetrics(buf)

	payload := []byte("hello world")
	n, err := rm.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned %d, want %d", n, len(payload))
	}
	if rm.BytesWritten() != len(payload) {
		t.Fatalf("BytesWritten = %d, want %d", rm.BytesWritten(), len(payload))
	}

	out := make([]byte, len(payload))
	n, err = rm.Read(out)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Read returned %d, want %d", n, len(payload))
	}
	if rm.BytesRead() != len(payload) {
		t.Fatalf("BytesRead = %d, want %d", rm.BytesRead(), len(payload))
	}
}

func TestReaderMetrics_Accumulates(t *testing.T) {
	buf := &fakeBuf{}
	rm := newReaderMetrics(buf)

	for i := 0; i < 5; i++ {
		_, _ = rm.Write([]byte("ab"))
	}
	if rm.BytesWritten() != 10 {
		t.Fatalf("BytesWritten = %d, want 10", rm.BytesWritten())
	}

	// Fill the buffer so reads get data.
	for i := 0; i < 3; i++ {
		_, _ = rm.Write([]byte("xy"))
	}
	out := make([]byte, 6)
	_, _ = rm.Read(out)
	if rm.BytesRead() != 6 {
		t.Fatalf("BytesRead = %d, want 6", rm.BytesRead())
	}
}

func TestReaderMetrics_WriteError_NotCounted(t *testing.T) {
	buf := &fakeBuf{}
	rm := newReaderMetrics(buf)

	_, _ = rm.Write([]byte("ok"))
	wantWritten := rm.BytesWritten()

	// Wrap in an error-returning writer.
	errRW := &errWriteCloser{ReadWriteCloser: buf}
	rm2 := newReaderMetrics(errRW)
	_, err := rm2.Write([]byte("fail"))
	if err == nil {
		t.Fatal("expected write error")
	}
	if rm2.BytesWritten() != 0 {
		t.Fatalf("BytesWritten should be 0 after error, got %d", rm2.BytesWritten())
	}
	_ = wantWritten
}

func TestReaderMetrics_ReadError_NotCounted(t *testing.T) {
	errRW := &errReadCloser{ReadWriteCloser: &fakeBuf{}}
	rm := newReaderMetrics(errRW)

	out := make([]byte, 4)
	_, err := rm.Read(out)
	if err == nil {
		t.Fatal("expected read error")
	}
	if rm.BytesRead() != 0 {
		t.Fatalf("BytesRead should be 0 after error, got %d", rm.BytesRead())
	}
}

func TestReaderMetrics_Close(t *testing.T) {
	buf := &fakeBuf{}
	rm := newReaderMetrics(buf)
	if err := rm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !buf.closed {
		t.Fatal("underlying closer was not called")
	}
}

// helpers for error injection.
type errWriteCloser struct{ io.ReadWriteCloser }

func (e *errWriteCloser) Write(_ []byte) (int, error) { return 0, errors.New("write error") }

type errReadCloser struct{ io.ReadWriteCloser }

func (e *errReadCloser) Read(_ []byte) (int, error) { return 0, errors.New("read error") }
