package pr0xteus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
)

const (
	// cellHealthzPath + cellStatusPath are the cellproxy control endpoints the
	// controller scrapes over the cell network.
	cellHealthzPath = "/healthz"
	cellStatusPath  = "/status"
)

// cellControlClient talks to one cell's cellproxy control HTTP server: a
// liveness probe (/healthz) and the full traffic snapshot (/status). It is a
// thin decoder over an injected HTTPDoer so the health loop, the API, and tests
// all share the same wire handling.
type cellControlClient struct {
	http HTTPDoer
}

// Status fetches and decodes a cell's /status view: identity, uptime, and the
// per-destination traffic snapshot.
func (c cellControlClient) Status(
	ctx context.Context, base *url.URL,
) (cellproxy.Status, error) {
	var status cellproxy.Status

	if base == nil {
		return status, ctxerrors.Wrap(commerr.ErrFetchFailed, "cell has no control url")
	}

	resp, err := c.get(ctx, base, cellStatusPath)
	if err != nil {
		return status, err
	}
	defer c.closeBody(ctx, resp)

	if resp.StatusCode != http.StatusOK {
		return status, ctxerrors.Wrapf(
			commerr.ErrFetchFailed, "cell status returned http %d", resp.StatusCode,
		)
	}

	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return status, ctxerrors.Wrap(err, "decode cell status")
	}

	return status, nil
}

// Healthy reports whether the cell's /healthz endpoint answers 200. Any
// transport error, non-200 status, or nil control URL is treated as unhealthy.
func (c cellControlClient) Healthy(ctx context.Context, base *url.URL) bool {
	if base == nil {
		return false
	}

	resp, err := c.get(ctx, base, cellHealthzPath)
	if err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"cell health probe failed", "err", err, "control_url", base.String(),
		)

		return false
	}
	defer c.closeBody(ctx, resp)

	return resp.StatusCode == http.StatusOK
}

// get issues a GET against base joined with path.
func (c cellControlClient) get(
	ctx context.Context, base *url.URL, path string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, base.JoinPath(path).String(), nil,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "new cell control request")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "do cell control request")
	}

	return resp, nil
}

// closeBody drains + closes a control response body, logging a close failure
// rather than dropping it silently.
func (c cellControlClient) closeBody(ctx context.Context, resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		ctxscope.GetLogger(ctx).Warn("closing cell control response failed", "err", err)
	}
}
