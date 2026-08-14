// Command cellproxy is the metrics-emitting SOCKS5 proxy that runs inside a
// pr0xteus cell in place of microsocks. See internal/pkg/cellproxy.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
	_ "github.com/psyb0t/slogging/slogconf"
)

// Set via `go build -ldflags "-X main.buildCommit=abc123"`.
//
//nolint:gochecknoglobals // build-time injected identity, must be package-global
var (
	appName     = "cellproxy"
	buildCommit string
)

const (
	scopeKeyBinary = "binary"
	scopeKeyCommit = "commit"
	scopeKeyCell   = "cell_id"
	scopeKeyParent = "parent_id"
)

func main() {
	if err := run(); err != nil {
		ctxscope.GetLogger(context.Background()).Error("cell proxy failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := cellproxy.LoadConfig()
	if err != nil {
		return ctxerrors.Wrap(err, "load cell proxy config")
	}

	setProcessScope(cfg)

	ctx, stop := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM,
	)
	defer stop()

	if err := cellproxy.New(cfg).Run(ctx); err != nil {
		return ctxerrors.Wrap(err, "run cell proxy")
	}

	return nil
}

func setProcessScope(cfg cellproxy.Config) {
	attrs := []ctxscope.Attribute{ctxscope.Attr(scopeKeyBinary, appName)}

	if buildCommit != "" {
		attrs = append(attrs, ctxscope.Attr(scopeKeyCommit, buildCommit))
	}

	if cfg.CellID != "" {
		attrs = append(attrs, ctxscope.Attr(scopeKeyCell, cfg.CellID))
	}

	if cfg.ParentID != "" {
		attrs = append(attrs, ctxscope.Attr(scopeKeyParent, cfg.ParentID))
	}

	ctxscope.SetGlobal(attrs...)
}
