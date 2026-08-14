//go:build integration

package controlapi

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/psyb0t/pr0xteus/tests/testinfra"
)

const (
	setupTimeout    = 5 * time.Minute
	teardownTimeout = time.Minute
)

var infra *testinfra.Infra

func TestMain(m *testing.M) {
	setupCtx, setupCancel := context.WithTimeout(context.Background(), setupTimeout)
	createdInfra, err := testinfra.Setup(setupCtx)
	setupCancel()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "integration test infrastructure setup failed:", err)
		os.Exit(1)
	}
	infra = createdInfra

	code := m.Run()

	teardownCtx, teardownCancel := context.WithTimeout(
		context.Background(), teardownTimeout,
	)
	if err := infra.Teardown(teardownCtx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "integration test infrastructure teardown failed:", err)
		code = 1
	}
	teardownCancel()

	os.Exit(code)
}
