//go:build integration

// Package testinfra brings up the real Docker, WireGuard, and SOCKS5
// dependencies used by pr0xteus integration tests.
package testinfra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"github.com/psyb0t/ctxerrors"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	workDirectoryName       = ".integration-work"
	workDirectoryParentMode = 0o755
	workDirectoryMode       = 0o777
	secretFileMode          = 0o444
	coverageDirectoryName   = "coverage"
	coverageDirectoryMode   = 0o777
	// coverageOutputEnv is where the servicepack coverage engine wants the
	// controller's native covdata written; it exports this before running the
	// tests, and we mount it into the controller container as GOCOVERDIR.
	coverageOutputEnv = "SERVICEPACK_COVDATA_DIR"

	controllerImageRepository    = "psyb0t/pr0xteus-integration"
	controllerDockerfile         = "Dockerfile"
	controllerCoverageDockerfile = "Dockerfile.integration"
	peerDockerfile               = "tests/testinfra/wireguard/Dockerfile"
	cellImage                    = "psyb0t/pr0xteus:cell-dev"
	curlImage                    = "curlimages/curl@sha256:d94d07ba9e7d6de898b6d96c1a072f6f8266c687af78a74f380087a0addf5d17"

	controllerAlias  = "pr0xteus-controller"
	peerAlias        = "wireguard-peer"
	peerTunnelTarget = "10.251.0.1:8080"

	dockerSocketPath = "/var/run/docker.sock"

	controllerStartupTimeout  = 3 * time.Minute
	networkDetachPollInterval = 100 * time.Millisecond
)

// ProxyAssignment is the production HTTP response returned after the
// controller creates a private, handshake-ready SOCKS5 cell.
type ProxyAssignment struct {
	URL         string `json:"url"`
	Pool        string `json:"pool"`
	ExitCountry string `json:"exitCountry"`
}

// ExternalProviderConfig identifies an operator-owned WireGuard bundle and
// its pool policy. It is only for opt-in integration tests that deliberately
// exercise a real provider; normal tests keep generating their own peer.
type ExternalProviderConfig struct {
	BundleDir   string
	PoolsFile   string
	RoutingFile string
}

// SetupOption configures one integration fixture before it creates any
// Testcontainers resource.
type SetupOption func(*setupOptions) error

type setupOptions struct {
	externalProvider *ExternalProviderConfig
}

// WithExternalProvider makes the fixture load an existing provider bundle
// rather than generate the self-contained WireGuard peer used by CI.
func WithExternalProvider(config ExternalProviderConfig) SetupOption {
	return func(options *setupOptions) error {
		if options.externalProvider != nil {
			return ctxerrors.New("external provider is already configured")
		}

		validated, err := validateExternalProviderConfig(config)
		if err != nil {
			return ctxerrors.Wrap(err, "validate external provider configuration")
		}

		options.externalProvider = validated

		return nil
	}
}

// Infra owns one full, isolated controller → WireGuard cell → SOCKS5 test
// stack. TestMain creates it once per package and must call Teardown.
type Infra struct {
	Controller testcontainers.Container
	Consumer   testcontainers.Container
	Peer       testcontainers.Container
	Network    *testcontainers.DockerNetwork

	APIToken string

	repositoryRoot   string
	workDir          string
	coverageDir      string
	coverageOutput   string
	externalProvider *ExternalProviderConfig
	peerTarget       string
}

// Setup creates a Testcontainers network, a real WireGuard peer, the
// production controller image, and a sibling SOCKS5 consumer. The WireGuard
// peer writes an ephemeral client config into a test-owned work directory;
// no provider configuration or credential is used. WithExternalProvider
// replaces only the generated peer with operator-supplied test configuration.
func Setup(ctx context.Context, options ...SetupOption) (*Infra, error) {
	root, err := repositoryRoot()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find repository root")
	}

	configured := setupOptions{}
	for _, option := range options {
		if option == nil {
			return nil, ctxerrors.New("integration setup option is nil")
		}

		if err := option(&configured); err != nil {
			return nil, ctxerrors.Wrap(err, "apply integration setup option")
		}
	}

	coverageOutput, err := validateCoverageOutput(root)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate controller coverage output")
	}

	infra := &Infra{
		repositoryRoot:   root,
		coverageOutput:   coverageOutput,
		externalProvider: configured.externalProvider,
	}
	if err := infra.prepareWorkDir(); err != nil {
		return nil, ctxerrors.Wrap(err, "prepare integration fixture")
	}

	if err := infra.setup(ctx); err != nil {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), controllerStartupTimeout,
		)
		defer cancel()

		if cleanupErr := infra.Teardown(cleanupCtx); cleanupErr != nil {
			return nil, errors.Join(
				ctxerrors.Wrap(err, "set up integration infrastructure"),
				ctxerrors.Wrap(cleanupErr, "clean up failed setup"),
			)
		}

		return nil, ctxerrors.Wrap(err, "set up integration infrastructure")
	}

	return infra, nil
}

func (i *Infra) setup(ctx context.Context) error {
	createdNetwork, err := network.New(ctx, network.WithAttachable())
	if err != nil {
		return ctxerrors.Wrap(err, "create integration network")
	}
	i.Network = createdNetwork

	if i.externalProvider == nil {
		if err := i.setupPeer(ctx); err != nil {
			return ctxerrors.Wrap(err, "start WireGuard peer")
		}
	}

	if err := i.setupController(ctx); err != nil {
		return ctxerrors.Wrap(err, "start controller")
	}

	if err := i.setupConsumer(ctx); err != nil {
		return ctxerrors.Wrap(err, "start SOCKS5 consumer")
	}

	return nil
}

func (i *Infra) setupPeer(ctx context.Context) error {
	peer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    i.repositoryRoot,
				Dockerfile: peerDockerfile,
				Repo:       controllerImageRepository + "-wireguard-peer",
				Tag:        filepath.Base(i.workDir),
				KeepImage:  false,
			},
			Networks: []string{i.Network.Name},
			NetworkAliases: map[string][]string{
				i.Network.Name: {peerAlias},
			},
			HostConfigModifier: func(hostConfig *container.HostConfig) {
				hostConfig.Binds = append(hostConfig.Binds, i.workDir+":/shared")
				hostConfig.CapDrop = []string{"ALL"}
				hostConfig.CapAdd = []string{"NET_ADMIN"}
				hostConfig.Devices = []container.DeviceMapping{{
					PathOnHost:        "/dev/net/tun",
					PathInContainer:   "/dev/net/tun",
					CgroupPermissions: "rwm",
				}}
				hostConfig.Sysctls = map[string]string{
					"net.ipv4.ip_forward": "1",
				}
			},
			WaitingFor: wait.ForLog("test WireGuard peer ready").
				WithStartupTimeout(controllerStartupTimeout),
		},
		Started: true,
	})
	if peer != nil {
		i.Peer = peer
	}
	if err != nil {
		logs, logsErr := containerLogs(ctx, peer)
		if logsErr != nil {
			return ctxerrors.Wrap(err, "create WireGuard peer container")
		}

		return ctxerrors.Wrapf(
			err,
			"create WireGuard peer container (startup logs: %q)",
			logs,
		)
	}

	// Target the peer's tunnel address instead of its Docker-network address.
	// That makes the assertion exercise only the WireGuard route and avoids a
	// host-bridge hairpin through the peer's eth0 address.
	i.peerTarget = peerTunnelTarget

	return nil
}

func containerLogs(ctx context.Context, container testcontainers.Container) (string, error) {
	if container == nil {
		return "", nil
	}

	reader, err := container.Logs(ctx)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read integration container logs")
	}

	logs, err := io.ReadAll(reader)
	if err != nil {
		if closeErr := reader.Close(); closeErr != nil {
			return "", errors.Join(
				ctxerrors.Wrap(err, "collect integration container logs"),
				ctxerrors.Wrap(closeErr, "close integration container logs"),
			)
		}

		return "", ctxerrors.Wrap(err, "collect integration container logs")
	}

	if err := reader.Close(); err != nil {
		return "", ctxerrors.Wrap(err, "close integration container logs")
	}

	return strings.TrimSpace(string(logs)), nil
}

func (i *Infra) setupController(ctx context.Context) error {
	socketGroupID, err := dockerSocketGroupID()
	if err != nil {
		return ctxerrors.Wrap(err, "inspect Docker socket group")
	}

	bundleDir, poolsFile, routingFile := i.controllerConfigPaths()

	dockerfile := controllerDockerfile
	env := map[string]string{
		"PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE": "true",
		"PR0XTEUS_API_TOKEN":                 i.APIToken,
		"PR0XTEUS_CELL_IMAGE":                cellImage,
		"PR0XTEUS_CELL_NETWORK":              i.Network.Name,
		"PR0XTEUS_DOCKER_HOST":               "unix:///var/run/docker.sock",
		"PR0XTEUS_MANAGED_SCOPE":             filepath.Base(i.workDir),
		"TUNNEL_POOL_DEFAULT_POOL":           "integration",
		"TUNNEL_POOL_BUNDLE_DIR":             bundleDir,
		"TUNNEL_POOL_LISTEN_ADDR":            ":8000",
		"TUNNEL_POOL_METRICS_ADDR":           ":9091",
		"TUNNEL_POOL_POOLS_FILE":             poolsFile,
		"TUNNEL_POOL_ROUTING_FILE":           routingFile,
		"TUNNEL_POOL_SPAWN_TIMEOUT":          "2m",
		"LOG_ADD_SOURCE":                     "true",
		"LOG_FORMAT":                         "json",
		"LOG_LEVEL":                          "info",
	}
	if i.coverageOutput != "" {
		dockerfile = controllerCoverageDockerfile
		env["GOCOVERDIR"] = "/coverage"
	}

	controller, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    i.repositoryRoot,
				Dockerfile: dockerfile,
				Repo:       controllerImageRepository,
				Tag:        filepath.Base(i.workDir),
				KeepImage:  false,
			},
			Env:      env,
			Networks: []string{i.Network.Name},
			NetworkAliases: map[string][]string{
				i.Network.Name: {controllerAlias},
			},
			HostConfigModifier: func(hostConfig *container.HostConfig) {
				hostConfig.Binds = append(
					hostConfig.Binds,
					dockerSocketPath+":"+dockerSocketPath,
					i.workDir+":"+i.workDir+":ro",
				)
				if i.externalProvider != nil {
					hostConfig.Binds = append(
						hostConfig.Binds,
						i.externalProvider.BundleDir+":"+i.externalProvider.BundleDir+":ro",
						i.externalProvider.PoolsFile+":"+i.externalProvider.PoolsFile+":ro",
						i.externalProvider.RoutingFile+":"+i.externalProvider.RoutingFile+":ro",
					)
				}
				if i.coverageDir != "" {
					hostConfig.Binds = append(
						hostConfig.Binds,
						i.coverageDir+":/coverage",
					)
				}
				hostConfig.GroupAdd = append(hostConfig.GroupAdd, socketGroupID)
			},
			WaitingFor: wait.ForLog("pr0xteus running, waiting for SIGTERM").
				WithStartupTimeout(controllerStartupTimeout),
		},
		Started: true,
	})
	if controller != nil {
		i.Controller = controller
	}
	if err != nil {
		logs, logsErr := containerLogs(ctx, controller)
		if logsErr != nil {
			return ctxerrors.Wrap(err, "create controller container")
		}

		return ctxerrors.Wrapf(
			err,
			"create controller container (startup logs: %q)",
			logs,
		)
	}

	return nil
}

func (i *Infra) controllerConfigPaths() (string, string, string) {
	if i.externalProvider != nil {
		return i.externalProvider.BundleDir,
			i.externalProvider.PoolsFile,
			i.externalProvider.RoutingFile
	}

	return i.workDir,
		filepath.Join(i.workDir, "pools.yaml"),
		filepath.Join(i.workDir, "routing.yaml")
}

func (i *Infra) setupConsumer(ctx context.Context) error {
	consumer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:      curlImage,
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{"while :; do sleep 600; done"},
			Networks:   []string{i.Network.Name},
		},
		Started: true,
	})
	if consumer != nil {
		i.Consumer = consumer
	}
	if err != nil {
		return ctxerrors.Wrap(err, "create SOCKS5 consumer container")
	}

	return nil
}

// AcquireProxy exercises the real private HTTP API from a sibling container,
// so no host port mapping or host-specific Docker endpoint assumption leaks
// into the test stack.
func (i *Infra) AcquireProxy(ctx context.Context) (ProxyAssignment, error) {
	return i.AcquireProxyForCountry(ctx, "ZZ")
}

// AcquireProxyForCountry exercises the real private HTTP API from a sibling
// container using the supplied country selector.
func (i *Infra) AcquireProxyForCountry(
	ctx context.Context, country string,
) (ProxyAssignment, error) {
	if i.Consumer == nil {
		return ProxyAssignment{}, ctxerrors.New("SOCKS5 consumer is not running")
	}

	payload, err := json.Marshal(struct {
		Country string `json:"country"`
	}{Country: country})
	if err != nil {
		return ProxyAssignment{}, ctxerrors.Wrap(err, "encode proxy assignment request")
	}

	exitCode, output, err := i.Consumer.Exec(ctx, []string{
		"curl",
		"--fail-with-body",
		"--silent",
		"--show-error",
		"--request", "POST",
		"--header", "Authorization: Bearer " + i.APIToken,
		"--header", "Content-Type: application/json",
		"--data", string(payload),
		"http://" + controllerAlias + ":8000/v1/proxies",
	}, tcexec.Multiplexed())
	if err != nil {
		return ProxyAssignment{}, ctxerrors.Wrap(err, "execute proxy assignment request")
	}

	body, err := io.ReadAll(output)
	if err != nil {
		return ProxyAssignment{}, ctxerrors.Wrap(err, "read proxy assignment response")
	}
	if exitCode != 0 {
		logs, logsErr := containerLogs(ctx, i.Controller)
		if logsErr != nil {
			return ProxyAssignment{}, ctxerrors.Wrapf(
				ctxerrors.New("non-success response"),
				"proxy assignment request failed: %s",
				strings.TrimSpace(string(body)),
			)
		}

		return ProxyAssignment{}, ctxerrors.Wrapf(
			ctxerrors.New("non-success response"),
			"proxy assignment request failed: %s (controller logs: %q)",
			strings.TrimSpace(string(body)),
			logs,
		)
	}

	var assignment ProxyAssignment
	if err := json.Unmarshal(body, &assignment); err != nil {
		return ProxyAssignment{}, ctxerrors.Wrap(err, "decode proxy assignment response")
	}

	return assignment, nil
}

// CellSummary is the subset of the controller's GET /v1/cells view the
// integration test asserts on.
type CellSummary struct {
	ContainerID string `json:"containerId"`
	Pool        string `json:"pool"`
	Traffic     *struct {
		Requests int64 `json:"requests"`
		Active   int64 `json:"active"`
	} `json:"traffic"`
}

// ListCells exercises GET /v1/cells from a sibling container, returning the
// controller's live view of every running cell (each carrying the traffic
// snapshot scraped from its cellproxy /status).
func (i *Infra) ListCells(ctx context.Context) ([]CellSummary, error) {
	if i.Consumer == nil {
		return nil, ctxerrors.New("SOCKS5 consumer is not running")
	}

	exitCode, output, err := i.Consumer.Exec(ctx, []string{
		"curl",
		"--fail-with-body",
		"--silent",
		"--show-error",
		"--header", "Authorization: Bearer " + i.APIToken,
		"http://" + controllerAlias + ":8000/v1/cells",
	}, tcexec.Multiplexed())
	if err != nil {
		return nil, ctxerrors.Wrap(err, "execute list-cells request")
	}

	body, err := io.ReadAll(output)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read list-cells response")
	}
	if exitCode != 0 {
		return nil, ctxerrors.Wrapf(
			ctxerrors.New("non-success response"),
			"list-cells request failed: %s", strings.TrimSpace(string(body)),
		)
	}

	var decoded struct {
		Cells []CellSummary `json:"cells"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, ctxerrors.Wrap(err, "decode list-cells response")
	}

	return decoded.Cells, nil
}

// DestroyCell exercises DELETE /v1/cells/{containerID} from a sibling container,
// destroying a cell on demand.
func (i *Infra) DestroyCell(ctx context.Context, containerID string) error {
	if i.Consumer == nil {
		return ctxerrors.New("SOCKS5 consumer is not running")
	}

	exitCode, output, err := i.Consumer.Exec(ctx, []string{
		"curl",
		"--fail-with-body",
		"--silent",
		"--show-error",
		"--request", "DELETE",
		"--header", "Authorization: Bearer " + i.APIToken,
		"http://" + controllerAlias + ":8000/v1/cells/" + containerID,
	}, tcexec.Multiplexed())
	if err != nil {
		return ctxerrors.Wrap(err, "execute destroy-cell request")
	}

	body, err := io.ReadAll(output)
	if err != nil {
		return ctxerrors.Wrap(err, "read destroy-cell response")
	}
	if exitCode != 0 {
		return ctxerrors.Wrapf(
			ctxerrors.New("non-success response"),
			"destroy-cell request failed: %s", strings.TrimSpace(string(body)),
		)
	}

	return nil
}

// GetCell exercises GET /v1/cells/{containerID} from a sibling container.
func (i *Infra) GetCell(
	ctx context.Context, containerID string,
) (CellSummary, error) {
	var cell CellSummary

	if i.Consumer == nil {
		return cell, ctxerrors.New("SOCKS5 consumer is not running")
	}

	exitCode, output, err := i.Consumer.Exec(ctx, []string{
		"curl",
		"--fail-with-body",
		"--silent",
		"--show-error",
		"--header", "Authorization: Bearer " + i.APIToken,
		"http://" + controllerAlias + ":8000/v1/cells/" + containerID,
	}, tcexec.Multiplexed())
	if err != nil {
		return cell, ctxerrors.Wrap(err, "execute get-cell request")
	}

	body, err := io.ReadAll(output)
	if err != nil {
		return cell, ctxerrors.Wrap(err, "read get-cell response")
	}
	if exitCode != 0 {
		return cell, ctxerrors.Wrapf(
			ctxerrors.New("non-success response"),
			"get-cell request failed: %s", strings.TrimSpace(string(body)),
		)
	}

	if err := json.Unmarshal(body, &cell); err != nil {
		return cell, ctxerrors.Wrap(err, "decode get-cell response")
	}

	return cell, nil
}

// PoolNames exercises GET /v1/pools from a sibling container, returning the
// configured pool names.
func (i *Infra) PoolNames(ctx context.Context) ([]string, error) {
	if i.Consumer == nil {
		return nil, ctxerrors.New("SOCKS5 consumer is not running")
	}

	exitCode, output, err := i.Consumer.Exec(ctx, []string{
		"curl",
		"--fail-with-body",
		"--silent",
		"--show-error",
		"--header", "Authorization: Bearer " + i.APIToken,
		"http://" + controllerAlias + ":8000/v1/pools",
	}, tcexec.Multiplexed())
	if err != nil {
		return nil, ctxerrors.Wrap(err, "execute list-pools request")
	}

	body, err := io.ReadAll(output)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read list-pools response")
	}
	if exitCode != 0 {
		return nil, ctxerrors.Wrapf(
			ctxerrors.New("non-success response"),
			"list-pools request failed: %s", strings.TrimSpace(string(body)),
		)
	}

	var decoded struct {
		Pools []struct {
			Name string `json:"name"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, ctxerrors.Wrap(err, "decode list-pools response")
	}

	names := make([]string, 0, len(decoded.Pools))
	for _, pool := range decoded.Pools {
		names = append(names, pool.Name)
	}

	return names, nil
}

// HealthzStatus hits the controller's unauthenticated metrics-listener /healthz.
func (i *Infra) HealthzStatus(ctx context.Context) (int, error) {
	return i.curlStatus(
		ctx, "GET", "http://"+controllerAlias+":9091/healthz", false,
	)
}

// MetricsStatus hits the controller's unauthenticated /metrics scrape endpoint.
func (i *Infra) MetricsStatus(ctx context.Context) (int, error) {
	return i.curlStatus(
		ctx, "GET", "http://"+controllerAlias+":9091/metrics", false,
	)
}

// UnauthenticatedCellsStatus hits GET /v1/cells with no bearer token, to prove
// the auth gate rejects it.
func (i *Infra) UnauthenticatedCellsStatus(ctx context.Context) (int, error) {
	return i.curlStatus(
		ctx, "GET", "http://"+controllerAlias+":8000/v1/cells", false,
	)
}

// curlStatus issues a request from the consumer and returns only the HTTP status
// code (body discarded), so callers can assert 200/401/etc. without --fail.
func (i *Infra) curlStatus(
	ctx context.Context, method, url string, authed bool,
) (int, error) {
	if i.Consumer == nil {
		return 0, ctxerrors.New("SOCKS5 consumer is not running")
	}

	args := []string{
		"curl", "--silent", "--output", "/dev/null",
		"--write-out", "%{http_code}", "--request", method,
	}
	if authed {
		args = append(args, "--header", "Authorization: Bearer "+i.APIToken)
	}

	args = append(args, url)

	exitCode, output, err := i.Consumer.Exec(ctx, args, tcexec.Multiplexed())
	if err != nil {
		return 0, ctxerrors.Wrap(err, "execute status request")
	}

	body, err := io.ReadAll(output)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "read status response")
	}
	if exitCode != 0 {
		return 0, ctxerrors.Wrapf(
			ctxerrors.New("curl transport failure"),
			"status probe exit %d: %s", exitCode, strings.TrimSpace(string(body)),
		)
	}

	code, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return 0, ctxerrors.Wrapf(err, "parse status code %q", string(body))
	}

	return code, nil
}

// AssertProxyEgress proves a returned private SOCKS5 URL routes a request
// through the WireGuard peer. The target is an isolated HTTP server on the
// peer itself, so CI never depends on a public API or provider account.
func (i *Infra) AssertProxyEgress(ctx context.Context, proxyAddress string) error {
	if i.Consumer == nil {
		return ctxerrors.New("SOCKS5 consumer is not running")
	}

	exitCode, output, err := i.Consumer.Exec(ctx, []string{
		"curl",
		"--connect-timeout", "20",
		"--fail",
		"--max-time", "45",
		"--output", "/dev/null",
		"--silent",
		"--show-error",
		"--socks5-hostname", proxyAddress,
		"http://" + i.peerTarget + "/healthz",
	}, tcexec.Multiplexed())
	if err != nil {
		return ctxerrors.Wrap(err, "execute SOCKS5 egress request")
	}

	var response []byte
	if output != nil {
		response, err = io.ReadAll(output)
		if err != nil {
			return ctxerrors.Wrap(err, "read SOCKS5 egress response")
		}
	}

	if exitCode != 0 {
		return ctxerrors.Wrapf(
			ctxerrors.New("SOCKS5 egress request failed"),
			"curl output: %s",
			strings.TrimSpace(string(response)),
		)
	}

	return nil
}

// Teardown removes exactly the Testcontainers resources this fixture created.
// The controller receives SIGTERM before the network disappears, letting its
// normal shutdown path remove its uniquely scoped cell.
func (i *Infra) Teardown(ctx context.Context) error {
	var errs []error

	if i.Consumer != nil {
		if err := i.Consumer.Terminate(ctx); err != nil {
			errs = append(errs, ctxerrors.Wrap(err, "terminate SOCKS5 consumer"))
		}
	}

	if i.Controller != nil {
		if err := i.Controller.Terminate(ctx); err != nil {
			errs = append(errs, ctxerrors.Wrap(err, "terminate controller"))
		}
	}

	if i.Peer != nil {
		if err := i.Peer.Terminate(ctx); err != nil {
			errs = append(errs, ctxerrors.Wrap(err, "terminate WireGuard peer"))
		}
	}

	if i.Network != nil {
		if err := i.waitForNetworkEndpointDetachment(ctx); err != nil {
			errs = append(
				errs,
				ctxerrors.Wrap(err, "wait for integration network endpoints"),
			)
		}

		if err := i.Network.Remove(ctx); err != nil {
			errs = append(errs, ctxerrors.Wrap(err, "remove integration network"))
		}
	}

	if err := i.collectControllerCoverage(); err != nil {
		errs = append(errs, ctxerrors.Wrap(err, "collect controller coverage"))
	}

	if err := i.removeWorkDir(); err != nil {
		errs = append(errs, ctxerrors.Wrap(err, "remove integration work directory"))
	}

	return errors.Join(errs...)
}

// waitForNetworkEndpointDetachment waits for Docker to finish detaching the
// controller-managed cell before Testcontainers removes its network. Docker's
// container remove call can return just before the network endpoint vanishes.
func (i *Infra) waitForNetworkEndpointDetachment(
	ctx context.Context,
) (returnErr error) {
	dockerClient, err := mobyclient.New(mobyclient.FromEnv)
	if err != nil {
		return ctxerrors.Wrap(err, "create Docker client for network inspection")
	}

	defer func() {
		if err := dockerClient.Close(); err != nil {
			returnErr = errors.Join(
				returnErr,
				ctxerrors.Wrap(err, "close Docker network inspection client"),
			)
		}
	}()

	ticker := time.NewTicker(networkDetachPollInterval)
	defer ticker.Stop()

	for {
		inspection, err := dockerClient.NetworkInspect(
			ctx,
			i.Network.ID,
			mobyclient.NetworkInspectOptions{},
		)
		if err != nil {
			return ctxerrors.Wrap(err, "inspect integration network")
		}

		if len(inspection.Network.Containers) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctxerrors.Wrap(
				ctx.Err(), "wait for integration network endpoints to detach",
			)
		case <-ticker.C:
		}
	}
}

// collectControllerCoverage moves native coverage files from the temporary
// fixture into the caller-provided ignored destination before teardown.
func (i *Infra) collectControllerCoverage() error {
	if i.coverageDir == "" || i.coverageOutput == "" {
		return nil
	}

	entries, err := os.ReadDir(i.coverageDir)
	if err != nil {
		return ctxerrors.Wrap(err, "read controller coverage directory")
	}
	if len(entries) == 0 {
		return ctxerrors.New("instrumented controller emitted no coverage data")
	}

	for _, entry := range entries {
		if entry.IsDir() {
			return ctxerrors.New("controller coverage directory contains a subdirectory")
		}

		source := filepath.Join(i.coverageDir, entry.Name())
		target := filepath.Join(i.coverageOutput, entry.Name())
		if err := os.Rename(source, target); err != nil {
			return ctxerrors.Wrap(err, "preserve controller coverage data")
		}
	}

	return nil
}

func (i *Infra) prepareWorkDir() error {
	parent := filepath.Join(i.repositoryRoot, workDirectoryName)
	if err := os.MkdirAll(parent, workDirectoryParentMode); err != nil {
		return ctxerrors.Wrap(err, "create integration work parent")
	}

	workDir, err := os.MkdirTemp(parent, "run-")
	if err != nil {
		return ctxerrors.Wrap(err, "create integration work directory")
	}
	i.workDir = workDir
	if i.coverageOutput != "" {
		i.coverageDir = filepath.Join(workDir, coverageDirectoryName)
		if err := os.Mkdir(i.coverageDir, coverageDirectoryMode); err != nil {
			return ctxerrors.Wrap(err, "create controller coverage directory")
		}
		if err := os.Chmod(i.coverageDir, coverageDirectoryMode); err != nil {
			return ctxerrors.Wrap(err, "set controller coverage directory permissions")
		}
	}

	if err := os.Chmod(workDir, workDirectoryMode); err != nil {
		return ctxerrors.Wrap(err, "set integration work directory permissions")
	}

	token, err := randomHex(32)
	if err != nil {
		return ctxerrors.Wrap(err, "generate integration API token")
	}
	i.APIToken = token

	if err := os.WriteFile(
		filepath.Join(workDir, "api-token"),
		[]byte(token),
		secretFileMode,
	); err != nil {
		return ctxerrors.Wrap(err, "write integration API token")
	}

	if i.externalProvider == nil {
		if err := os.WriteFile(
			filepath.Join(workDir, "pools.yaml"),
			[]byte("pools:\n  integration:\n    region: test\n    configs: [integration]\n    exit_countries:\n      integration: ZZ\n"),
			secretFileMode,
		); err != nil {
			return ctxerrors.Wrap(err, "write integration pools")
		}

		if err := os.WriteFile(
			filepath.Join(workDir, "routing.yaml"),
			[]byte("country_to_pool:\n  ZZ: integration\n"),
			secretFileMode,
		); err != nil {
			return ctxerrors.Wrap(err, "write integration routing")
		}
	}

	return nil
}

func validateExternalProviderConfig(
	config ExternalProviderConfig,
) (*ExternalProviderConfig, error) {
	if err := validateReadableDirectory(config.BundleDir, "external bundle directory"); err != nil {
		return nil, err
	}
	if err := validateReadableFile(config.PoolsFile, "external pools file"); err != nil {
		return nil, err
	}
	if err := validateReadableFile(config.RoutingFile, "external routing file"); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(config.BundleDir)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read external bundle directory")
	}

	configCount := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}

		path := filepath.Join(config.BundleDir, entry.Name())
		if err := validateReadableFile(path, "external WireGuard configuration"); err != nil {
			return nil, err
		}
		configCount++
	}
	if configCount == 0 {
		return nil, ctxerrors.New("external bundle directory has no .conf files")
	}

	return &config, nil
}

func validateReadableDirectory(path, label string) error {
	if !filepath.IsAbs(path) {
		return ctxerrors.Wrapf(
			ctxerrors.New("path must be absolute"), "%s: %s", label, path,
		)
	}

	info, err := os.Stat(path)
	if err != nil {
		return ctxerrors.Wrapf(err, "stat %s", label)
	}
	if !info.IsDir() {
		return ctxerrors.Wrapf(
			ctxerrors.New("path is not a directory"), "%s: %s", label, path,
		)
	}

	if _, err := os.ReadDir(path); err != nil {
		return ctxerrors.Wrapf(err, "read %s", label)
	}

	return nil
}

func validateReadableFile(path, label string) error {
	if !filepath.IsAbs(path) {
		return ctxerrors.Wrapf(
			ctxerrors.New("path must be absolute"), "%s: %s", label, path,
		)
	}

	info, err := os.Stat(path)
	if err != nil {
		return ctxerrors.Wrapf(err, "stat %s", label)
	}
	if !info.Mode().IsRegular() {
		return ctxerrors.Wrapf(
			ctxerrors.New("path is not a regular file"), "%s: %s", label, path,
		)
	}

	file, err := os.Open(path)
	if err != nil {
		return ctxerrors.Wrapf(err, "open %s", label)
	}
	if err := file.Close(); err != nil {
		return ctxerrors.Wrapf(err, "close %s", label)
	}

	return nil
}

func (i *Infra) removeWorkDir() error {
	if i.workDir == "" {
		return nil
	}

	parent := filepath.Join(i.repositoryRoot, workDirectoryName)
	relativePath, err := filepath.Rel(parent, i.workDir)
	if err != nil {
		return ctxerrors.Wrap(err, "resolve integration work path")
	}

	if relativePath == "." || filepath.IsAbs(relativePath) ||
		strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) ||
		!strings.HasPrefix(filepath.Base(i.workDir), "run-") {
		return ctxerrors.New("refuse to remove unexpected integration work path")
	}

	info, err := os.Stat(i.workDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return ctxerrors.Wrap(err, "stat integration work directory")
	}
	if !info.IsDir() {
		return ctxerrors.New("integration work path is not a directory")
	}

	if _, err := os.ReadDir(i.workDir); err != nil {
		return ctxerrors.Wrap(err, "inspect integration work directory")
	}

	if err := os.RemoveAll(i.workDir); err != nil {
		return ctxerrors.Wrap(err, "remove integration work directory")
	}

	return nil
}

func validateCoverageOutput(repositoryRoot string) (string, error) {
	destination := os.Getenv(coverageOutputEnv)
	if destination == "" {
		return "", nil
	}

	// The servicepack coverage engine points SERVICEPACK_COVDATA_DIR at a
	// directory under the repo's .cover tree; keep it confined there so a stray
	// value can't make the controller write covdata anywhere on the host.
	coverageRoot := filepath.Join(repositoryRoot, ".cover")
	relativePath, err := filepath.Rel(coverageRoot, destination)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve controller coverage output")
	}
	if relativePath == "." || filepath.IsAbs(relativePath) ||
		strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) ||
		filepath.Base(destination) == "." {
		return "", ctxerrors.New("controller coverage output must be a child of .cover")
	}

	info, err := os.Stat(destination)
	if err != nil {
		return "", ctxerrors.Wrap(err, "stat controller coverage output")
	}
	if !info.IsDir() {
		return "", ctxerrors.New("controller coverage output is not a directory")
	}

	return destination, nil
}

func repositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", ctxerrors.Wrap(err, "get working directory")
	}

	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}

		parent := filepath.Dir(directory)
		if parent == directory {
			return "", ctxerrors.New("go.mod not found above integration test")
		}

		directory = parent
	}
}

func dockerSocketGroupID() (string, error) {
	info, err := os.Stat(dockerSocketPath)
	if err != nil {
		return "", ctxerrors.Wrap(err, "stat Docker socket")
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ctxerrors.New("Docker socket does not expose Unix ownership")
	}

	return strconv.FormatUint(uint64(stat.Gid), 10), nil
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", ctxerrors.Wrap(err, "read random bytes")
	}

	return hex.EncodeToString(value), nil
}
