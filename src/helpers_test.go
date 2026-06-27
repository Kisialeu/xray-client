package main

import (
	"context"
	"io"
	"sync"

	"github.com/goxray/core/network/route"
)

// ── fake pipe ─────────────────────────────────────────────────────────────────

type fakePipe struct {
	mu      sync.Mutex
	copies  int
	blockCh chan struct{} // if non-nil, Copy blocks until it is closed
	err     error
}

func newFakePipe() *fakePipe                  { return &fakePipe{} }
func newBlockingPipe() *fakePipe              { return &fakePipe{blockCh: make(chan struct{})} }
func (p *fakePipe) unblock()                  { close(p.blockCh) }
func (p *fakePipe) Copies() int               { p.mu.Lock(); defer p.mu.Unlock(); return p.copies }

func (p *fakePipe) Copy(ctx context.Context, _ io.ReadWriteCloser, _ string) error {
	p.mu.Lock()
	p.copies++
	p.mu.Unlock()
	if p.blockCh != nil {
		select {
		case <-ctx.Done():
		case <-p.blockCh:
		}
	}
	return p.err
}

// ── fake route table ──────────────────────────────────────────────────────────

type fakeRoutes struct {
	mu      sync.Mutex
	added   []route.Opts
	deleted []route.Opts
	addErr  error
	delErr  error
}

func (r *fakeRoutes) Add(o route.Opts) error {
	r.mu.Lock(); defer r.mu.Unlock()
	r.added = append(r.added, o)
	return r.addErr
}
func (r *fakeRoutes) Delete(o route.Opts) error {
	r.mu.Lock(); defer r.mu.Unlock()
	r.deleted = append(r.deleted, o)
	return r.delErr
}
func (r *fakeRoutes) AddCount() int    { r.mu.Lock(); defer r.mu.Unlock(); return len(r.added) }
func (r *fakeRoutes) DeleteCount() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.deleted) }

// ── fake runnable (xray instance) ─────────────────────────────────────────────

type fakeRunnable struct {
	mu       sync.Mutex
	started  int
	closed   int
	startErr error
}

func (f *fakeRunnable) Start() error {
	f.mu.Lock(); defer f.mu.Unlock()
	f.started++
	return f.startErr
}
func (f *fakeRunnable) Close() error {
	f.mu.Lock(); defer f.mu.Unlock()
	f.closed++
	return nil
}

// ── fake ReadWriteCloser (in-memory buffer) ───────────────────────────────────

type fakeBuf struct {
	mu     sync.Mutex
	buf    []byte
	closed bool
}

func (b *fakeBuf) Write(p []byte) (int, error) {
	b.mu.Lock(); defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}
func (b *fakeBuf) Read(p []byte) (int, error) {
	b.mu.Lock(); defer b.mu.Unlock()
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}
func (b *fakeBuf) Close() error {
	b.mu.Lock(); defer b.mu.Unlock()
	b.closed = true
	return nil
}
