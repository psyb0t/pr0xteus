package cellproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	socks5 "github.com/things-go/go-socks5"
)

const (
	pathHealthz             = "/healthz"
	pathStatus              = "/status"
	controlReadHeaderWait   = 5 * time.Second
	controlShutdownDeadline = 5 * time.Second
	serverErrorChannelSize  = 2
)

// Status is the full cell picture returned by GET /status: identity, uptime, and
// the traffic snapshot. GET /v1/cells/{id} on the controller proxies to it.
type Status struct {
	CellID        string  `json:"cellId"`
	ParentID      string  `json:"parentId,omitempty"`
	StartedAt     string  `json:"startedAt"`
	UptimeSeconds float64 `json:"uptimeSeconds"`
	Traffic       Stats   `json:"traffic"`
}

// Config is the cell proxy's runtime configuration.
type Config struct {
	// CellID is this cell's own identifier (its short container ID, from the
	// hostname). ParentID is the spawning controller's container ID. Both are
	// echoed in /stats and tag the cell's logs for correlation.
	CellID   string
	ParentID string

	// SOCKSNetwork/SOCKSAddr are where the SOCKS5 proxy listens (the egress
	// interface inside the cell).
	SOCKSNetwork string
	SOCKSAddr    string

	// ControlAddr is where the metrics + health HTTP server listens. It must be
	// bound to the internal control interface only, never the egress side.
	ControlAddr string

	// DialTimeout bounds each outbound dial through the tunnel.
	DialTimeout time.Duration

	// TopDestinations caps the destination breakdown in a /stats snapshot.
	TopDestinations int
}

// Server runs the SOCKS5 proxy and the control HTTP server against one shared
// Recorder.
type Server struct {
	cfg       Config
	recorder  *Recorder
	socks     *socks5.Server
	startedAt time.Time
}

// New builds a cell proxy from cfg.
func New(cfg Config) *Server {
	recorder := NewRecorder(cfg.TopDestinations)
	server := &Server{cfg: cfg, recorder: recorder, startedAt: time.Now()}
	dialer := &net.Dialer{Timeout: cfg.DialTimeout}

	server.socks = socks5.NewServer(
		socks5.WithDial(server.dial(dialer)),
		socks5.WithLogger(newSocksLogger(context.Background())),
	)

	return server
}

// dial returns the metrics-recording dialer go-socks5 uses for CONNECT. A failed
// dial is recorded (a tunnel-liveness signal) before the error propagates.
func (s *Server) dial(
	dialer *net.Dialer,
) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			s.recorder.DialFailed()

			return nil, ctxerrors.Wrapf(err, "dial %s", addr)
		}

		key := s.recorder.Open(addr)

		return newCountingConn(conn, s.recorder, key), nil
	}
}

// Run starts both listeners and blocks until ctx is cancelled or a server fails.
func (s *Server) Run(ctx context.Context) error {
	logger := ctxscope.GetLogger(ctx)

	var listenConfig net.ListenConfig

	socksListener, err := listenConfig.Listen(ctx, s.cfg.SOCKSNetwork, s.cfg.SOCKSAddr)
	if err != nil {
		return ctxerrors.Wrap(err, "listen socks5")
	}

	control := &http.Server{
		Addr:              s.cfg.ControlAddr,
		Handler:           s.controlMux(),
		ReadHeaderTimeout: controlReadHeaderWait,
	}

	errCh := make(chan error, serverErrorChannelSize)
	s.serve(errCh, "socks5", func() error { return s.socks.Serve(socksListener) })
	s.serve(errCh, "control", control.ListenAndServe)

	logger.Info(
		"cell proxy started",
		"socks_addr", s.cfg.SOCKSAddr,
		"control_addr", s.cfg.ControlAddr,
	)

	select {
	case <-ctx.Done():
		logger.Info("cell proxy shutting down")
		s.shutdown(ctx, socksListener, control)

		return nil
	case runErr := <-errCh:
		s.shutdown(ctx, socksListener, control)

		return runErr
	}
}

// serve runs one server in a panic-recovering goroutine; a clean shutdown
// (listener closed / ErrServerClosed) is not reported as an error.
func (s *Server) serve(errCh chan<- error, name string, run func() error) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- ctxerrors.New(
					fmt.Sprintf("%s server panicked: %v\n%s", name, r, debug.Stack()),
				)
			}
		}()

		if err := run(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) &&
			!errors.Is(err, net.ErrClosed) {
			errCh <- ctxerrors.Wrapf(err, "%s server", name)
		}
	}()
}

func (s *Server) shutdown(
	ctx context.Context, socksListener net.Listener, control *http.Server,
) {
	if err := socksListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Warn("closing socks5 listener failed", "err", err)
	}

	// Detach from ctx (already cancelled during shutdown) so graceful shutdown
	// keeps its full deadline while still inheriting any ctx values.
	shutCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), controlShutdownDeadline,
	)
	defer cancel()

	if err := control.Shutdown(shutCtx); err != nil {
		slog.Warn("control server shutdown failed", "err", err)
	}
}

func (s *Server) controlMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc(http.MethodGet+" "+pathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)

		if _, err := w.Write([]byte("ok\n")); err != nil {
			slog.Debug("healthz write failed", "err", err)
		}
	})

	mux.HandleFunc(http.MethodGet+" "+pathStatus, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		status := Status{
			CellID:        s.cfg.CellID,
			ParentID:      s.cfg.ParentID,
			StartedAt:     s.startedAt.UTC().Format(time.RFC3339),
			UptimeSeconds: time.Since(s.startedAt).Seconds(),
			Traffic:       s.recorder.Snapshot(),
		}

		if err := json.NewEncoder(w).Encode(status); err != nil {
			ctxscope.GetLogger(r.Context()).Warn("status encode failed", "err", err)
		}
	})

	return mux
}

// socksLogger adapts go-socks5's Logger without exposing client-supplied data.
type socksLogger struct {
	log func(socks5FailureReason)
}

func (l socksLogger) Errorf(format string, args ...any) {
	l.log(classifySOCKS5Failure(format, args...))
}

func newSocksLogger(ctx context.Context) socksLogger {
	return socksLogger{log: func(reason socks5FailureReason) {
		logger := ctxscope.GetLogger(ctx)
		if reason == socks5FailureClientDisconnected {
			logger.Debug("cell SOCKS5 client disconnected", "reason", reason)

			return
		}

		logger.Warn("cell SOCKS5 request failed", "reason", reason)
	}}
}

const (
	socks5FailureAuthenticationFailed socks5FailureReason = "authentication_failed"
	socks5FailureClientDisconnected   socks5FailureReason = "client_disconnected"
	socks5FailureProtocolError        socks5FailureReason = "protocol_error"
	socks5FailureUpstreamConnect      socks5FailureReason = "upstream_connect_failed"
)

type socks5FailureReason string

func classifySOCKS5Failure(format string, args ...any) socks5FailureReason {
	message := fmt.Sprintf(format, args...)

	switch {
	case strings.Contains(message, "EOF"):
		return socks5FailureClientDisconnected
	case strings.Contains(message, "failed to authenticate"):
		return socks5FailureAuthenticationFailed
	case strings.Contains(message, "connect to"):
		return socks5FailureUpstreamConnect
	default:
		return socks5FailureProtocolError
	}
}
