//go:build integration && real

package real

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/pr0xteus/tests/testinfra"
	"github.com/stretchr/testify/require"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

const (
	realSetupTimeout    = 5 * time.Minute
	realTeardownTimeout = time.Minute
	realRequestTimeout  = 3 * time.Minute

	realTestEnabledEnv = "PR0XTEUS_REAL_TEST_ENABLED"
	realTestCountryEnv = "PR0XTEUS_REAL_TEST_COUNTRY"
	realDefaultCountry = "US"
	publicIPEndpoint   = "https://api.ipify.org"

	providerBundlePath = "secrets/wg/provider-wireguard"
	providerPoolsPath  = "secrets/wg/pools.yaml"
	routingPath        = "config/egress-routing.yaml"
)

var (
	realInfra   *testinfra.Infra
	realCountry string
)

func TestMain(m *testing.M) {
	if os.Getenv(realTestEnabledEnv) != "true" {
		_, _ = fmt.Fprintln(
			os.Stderr,
			"real external-provider test requires PR0XTEUS_REAL_TEST_ENABLED=true",
		)
		os.Exit(1)
	}

	country, err := realCountryFromEnvironment()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "real external-provider test setup failed:", err)
		os.Exit(1)
	}

	root, err := repositoryRoot()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "real external-provider test setup failed:", err)
		os.Exit(1)
	}

	setupCtx, setupCancel := context.WithTimeout(
		context.Background(), realSetupTimeout,
	)
	createdInfra, err := testinfra.Setup(
		setupCtx,
		testinfra.WithExternalProvider(testinfra.ExternalProviderConfig{
			BundleDir:   filepath.Join(root, providerBundlePath),
			PoolsFile:   filepath.Join(root, providerPoolsPath),
			RoutingFile: filepath.Join(root, routingPath),
		}),
	)
	setupCancel()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "real external-provider test setup failed:", err)
		os.Exit(1)
	}

	realInfra = createdInfra
	realCountry = country
	code := m.Run()

	teardownCtx, teardownCancel := context.WithTimeout(
		context.Background(), realTeardownTimeout,
	)
	if err := realInfra.Teardown(teardownCtx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "real external-provider test teardown failed:", err)
		code = 1
	}
	teardownCancel()

	os.Exit(code)
}

func TestRealExternalProvider_ChangesPublicEgressIP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), realRequestTimeout)
	t.Cleanup(cancel)

	assignment, err := realInfra.AcquireProxyForCountry(ctx, realCountry)
	require.NoError(t, err)
	require.NotEmpty(t, assignment.Pool)
	require.NotEmpty(t, assignment.ExitCountry)

	proxyURL, err := url.Parse(assignment.URL)
	require.NoError(t, err)
	require.Equal(t, "socks5", proxyURL.Scheme)
	require.NotEmpty(t, proxyURL.Host)

	directIP, err := observedPublicIP(ctx, "", true)
	require.NoError(t, err)
	proxiedIP, err := observedPublicIP(ctx, assignment.URL, false)
	require.NoError(t, err)
	require.NotEqual(t, directIP, proxiedIP)
}

func observedPublicIP(
	ctx context.Context, proxyAddress string, direct bool,
) (netip.Addr, error) {
	arguments := []string{
		"curl",
		"--ipv4",
		"--connect-timeout", "20",
		"--fail",
		"--max-time", "60",
		"--silent",
		"--show-error",
	}
	if proxyAddress != "" {
		arguments = append(arguments, "--proxy", proxyAddress)
	}
	arguments = append(arguments, publicIPEndpoint)

	consumer := realInfra.Consumer
	if direct {
		consumer = realInfra.DirectConsumer
	}
	if consumer == nil {
		return netip.Addr{}, ctxerrors.New("public IP test consumer is not running")
	}

	exitCode, output, err := consumer.Exec(
		ctx, arguments, tcexec.Multiplexed(),
	)
	if err != nil {
		return netip.Addr{}, ctxerrors.Wrap(err, "query public IP from test consumer")
	}

	body, err := io.ReadAll(output)
	if err != nil {
		return netip.Addr{}, ctxerrors.Wrap(err, "read public IP response")
	}
	if exitCode != 0 {
		return netip.Addr{}, ctxerrors.Wrapf(
			ctxerrors.New("public IP request failed"),
			"curl output: %s", strings.TrimSpace(string(body)),
		)
	}

	address, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, ctxerrors.Wrap(err, "parse public IP response")
	}

	return address.Unmap(), nil
}

func realCountryFromEnvironment() (string, error) {
	country := strings.ToUpper(strings.TrimSpace(os.Getenv(realTestCountryEnv)))
	if country == "" {
		country = realDefaultCountry
	}
	if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' ||
		country[1] < 'A' || country[1] > 'Z' {
		return "", ctxerrors.Wrapf(
			ctxerrors.New("country must be ISO alpha-2"),
			"%s=%q", realTestCountryEnv, country,
		)
	}

	return country, nil
}

func repositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", ctxerrors.Wrap(err, "get real test working directory")
	}

	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}

		parent := filepath.Dir(directory)
		if parent == directory {
			return "", ctxerrors.New("go.mod not found above real test")
		}

		directory = parent
	}
}
