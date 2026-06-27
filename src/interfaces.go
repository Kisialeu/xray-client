package main

import (
	"context"
	"io"

	"github.com/goxray/core/network/route"
)

type pipeIface interface {
	Copy(ctx context.Context, pipe io.ReadWriteCloser, socks5 string) error
}

type ipTable interface {
	Add(options route.Opts) error
	Delete(options route.Opts) error
}
