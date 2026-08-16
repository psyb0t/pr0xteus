package pr0xteus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	mobynet "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

// loopbackBindAddr is the host IP the cell container's port
// mappings bind to. Loopback keeps the proxy unreachable from
// outside the host.
//
//nolint:gochecknoglobals // package-level addr constant
var loopbackBindAddr = netip.MustParseAddr("127.0.0.1")

// DockerClient is the subset of the moby docker client used by the
// spawner. Pulled out so tests can inject a fake without dragging
// the entire SDK surface.
type DockerClient interface {
	ContainerCreate(
		ctx context.Context, options client.ContainerCreateOptions,
	) (client.ContainerCreateResult, error)

	ContainerStart(
		ctx context.Context, containerID string,
		options client.ContainerStartOptions,
	) (client.ContainerStartResult, error)

	ContainerLogs(
		ctx context.Context, containerID string,
		options client.ContainerLogsOptions,
	) (client.ContainerLogsResult, error)

	ContainerStop(
		ctx context.Context, containerID string,
		options client.ContainerStopOptions,
	) (client.ContainerStopResult, error)

	ContainerRemove(
		ctx context.Context, containerID string,
		options client.ContainerRemoveOptions,
	) (client.ContainerRemoveResult, error)

	ContainerInspect(
		ctx context.Context, containerID string,
		options client.ContainerInspectOptions,
	) (client.ContainerInspectResult, error)

	ContainerList(
		ctx context.Context, options client.ContainerListOptions,
	) (client.ContainerListResult, error)
}

// Docker container labels stamped onto every cell pr0xteus spawns.
// The orphan reconciler scans by LabelManaged so reaping survives
// process restarts (in-memory PoolState is rebuilt from scratch on
// boot, but docker remembers).
const (
	cellMemoryLimitBytes   = 128 * 1024 * 1024
	cellNanoCPUs           = 500 * 1000 * 1000
	cellPIDsLimit          = 64
	cellLogMaxSize         = "10m"
	cellLogMaxFiles        = "5"
	cellCapabilityNetAdmin = "NET_ADMIN"
	cellCapabilitySetGID   = "SETGID"
	cellCapabilitySetUID   = "SETUID"

	LabelManaged = "pr0xteus.managed"
	LabelPool    = "pr0xteus.pool"
	LabelConf    = "pr0xteus.conf"
	LabelScope   = "pr0xteus.scope"

	// LabelParent records the spawning controller's own container ID so the
	// controller can rediscover its children with a docker label filter and a
	// cell can report which parent it belongs to.
	LabelParent = "pr0xteus.parent.id"

	// Cell environment variable names. cellproxy reads its listen ports and its
	// parent identity from these; the entrypoint reads the ports for the iptables
	// kill-switch rules.
	envCellSocksPort   = "PR0XTEUS_SOCKS5_PORT"
	envCellControlPort = "PR0XTEUS_CELL_CONTROL_PORT"
	envCellParentID    = "PR0XTEUS_PARENT_ID"

	// controlURLScheme is the scheme of the cell control HTTP base URL.
	controlURLScheme = "http"
)

// HTTPDoer is the subset of *http.Client used by the spawner to
// probe the cell's SOCKS5 port for handshake readiness.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// CellSpawner is the production Spawner backed by the docker SDK.
// One per service; safe for concurrent use because every method
// dials the docker daemon directly.
type CellSpawner struct {
	cfg    Config
	docker DockerClient

	// http is used to probe the cell's SOCKS5 port (default :8000)
	// for handshake status.
	http HTTPDoer

	// nowFn lets tests pin time. Defaults to time.Now.
	nowFn func() time.Time
}

// cellLogCapture follows a just-started cell only until it either becomes
// ready or fails. A failing cell has AutoRemove enabled, so this preserves the
// startup cause before cleanup removes the exact test-owned container.
type cellLogCapture struct {
	cancel context.CancelFunc
	done   chan struct{}
	output bytes.Buffer
	err    error
}

// NewCellSpawner constructs a production spawner. Returns an
// error if the docker daemon is unreachable.
func NewCellSpawner(cfg Config) (*CellSpawner, error) {
	opts := []client.Opt{}

	if cfg.DockerHost != "" {
		opts = append(opts, client.WithHost(cfg.DockerHost))
	}

	cli, err := client.New(opts...)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "new docker client")
	}

	return &CellSpawner{
		cfg:    cfg,
		docker: cli,
		http:   &http.Client{},
		nowFn:  time.Now,
	}, nil
}

// Spawn runs the docker create + start + readiness-probe cycle.
// On any failure the partially-started container is best-effort
// killed before returning.
func (s *CellSpawner) Spawn(
	ctx context.Context, req SpawnRequest,
) (*Tunnel, error) {
	containerID, containerName, err := s.createAndStart(ctx, req)
	if err != nil {
		return nil, err
	}

	logs := s.startCellLogCapture(ctx, containerID)
	tunnel, err := s.waitReady(ctx, containerID, containerName, req)
	startupLogs, logsErr := logs.stop()

	if err != nil {
		s.bestEffortKill(ctx, containerID)

		if logsErr != nil {
			return nil, ctxerrors.Wrapf(
				err, "capture startup logs for container %s: %s",
				containerID, logsErr.Error(),
			)
		}

		if startupLogs != "" {
			return nil, ctxerrors.Wrapf(
				err, "container %s startup logs: %q",
				containerID, startupLogs,
			)
		}

		return nil, err
	}

	if logsErr != nil {
		ctxscope.GetLogger(ctx).Warn(
			"cell startup log capture stopped unexpectedly",
			"container_id", containerID,
			"err", logsErr,
		)
	}

	return tunnel, nil
}

func (s *CellSpawner) createAndStart(
	ctx context.Context, req SpawnRequest,
) (string, string, error) {
	containerName := buildContainerName(req.Pool, req.ConfName, s.nowFn())
	confPath := filepath.Join(req.BundleDir, req.ConfName+".conf")

	createResp, err := s.docker.ContainerCreate(
		ctx, client.ContainerCreateOptions{
			Name:             containerName,
			Config:           s.buildContainerConfig(req),
			HostConfig:       s.buildHostConfig(confPath),
			NetworkingConfig: s.buildNetworkingConfig(),
		},
	)
	if err != nil {
		return "", "", ctxerrors.Wrapf(
			ErrSpawnFailed, "container create: %s", err.Error(),
		)
	}

	if _, err := s.docker.ContainerStart(
		ctx, createResp.ID, client.ContainerStartOptions{},
	); err != nil {
		s.bestEffortKill(ctx, createResp.ID)

		return "", "", ctxerrors.Wrapf(
			ErrSpawnFailed, "container start %s: %s",
			createResp.ID, err.Error(),
		)
	}

	return createResp.ID, containerName, nil
}

func (s *CellSpawner) startCellLogCapture(
	ctx context.Context, containerID string,
) *cellLogCapture {
	logCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	capture := &cellLogCapture{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go func() {
		defer close(capture.done)

		reader, err := s.docker.ContainerLogs(
			logCtx,
			containerID,
			client.ContainerLogsOptions{
				Follow:     true,
				ShowStderr: true,
				ShowStdout: true,
			},
		)
		if err != nil {
			capture.err = ctxerrors.Wrap(err, "open cell startup log stream")

			return
		}

		_, copyErr := stdcopy.StdCopy(&capture.output, &capture.output, reader)
		closeErr := reader.Close()

		if copyErr != nil && !errors.Is(copyErr, context.Canceled) {
			capture.err = ctxerrors.Wrap(copyErr, "copy cell startup logs")

			return
		}

		if closeErr != nil && !errors.Is(closeErr, context.Canceled) {
			capture.err = ctxerrors.Wrap(closeErr, "close cell startup log stream")
		}
	}()

	return capture
}

func (c *cellLogCapture) stop() (string, error) {
	c.cancel()
	<-c.done

	return strings.TrimSpace(c.output.String()), c.err
}

// Kill stops + removes the named container. Idempotent — missing
// containers are NOT surfaced as errors.
func (s *CellSpawner) Kill(
	ctx context.Context, containerID string,
) error {
	stopTimeout := 10
	if _, err := s.docker.ContainerStop(
		ctx, containerID,
		client.ContainerStopOptions{Timeout: &stopTimeout},
	); err != nil && !isNotFound(err) {
		return ctxerrors.Wrapf(
			err, "stop container %s", containerID,
		)
	}

	if _, err := s.docker.ContainerRemove(
		ctx, containerID,
		client.ContainerRemoveOptions{Force: true},
	); err != nil && !isNotFound(err) && !isRemovalInProgress(err) {
		return ctxerrors.Wrapf(
			err, "remove container %s", containerID,
		)
	}

	return nil
}

// buildContainerConfig fills the docker Container config. cellproxy reads its
// SOCKS5 + control ports and its parent identity from the environment; the
// entrypoint reads the ports for its iptables kill-switch. Labels are how the
// reconciler finds + reaps orphan cells across process restarts, and how the
// controller rediscovers the children of this specific parent.
func (s *CellSpawner) buildContainerConfig(
	req SpawnRequest,
) *container.Config {
	socksPort := mustParsePort(s.cfg.CellSocksPort)
	controlPort := mustParsePort(s.cfg.CellControlPort)

	env := []string{
		envCellSocksPort + "=" + strconvItoa(s.cfg.CellSocksPort),
		envCellControlPort + "=" + strconvItoa(s.cfg.CellControlPort),
	}

	labels := map[string]string{
		LabelManaged: "true",
		LabelPool:    req.Pool,
		LabelConf:    req.ConfName,
		LabelScope:   s.cfg.ManagedScope,
	}

	if s.cfg.ParentID != "" {
		env = append(env, envCellParentID+"="+s.cfg.ParentID)
		labels[LabelParent] = s.cfg.ParentID
	}

	return &container.Config{
		Image: s.cfg.CellImage,
		Env:   env,
		ExposedPorts: mobynet.PortSet{
			socksPort:   struct{}{},
			controlPort: struct{}{},
		},
		Labels: labels,
	}
}

// buildHostConfig grants the one capability and device a WireGuard cell needs,
// then constrains memory, CPU, process count, and retained logs. The root
// filesystem remains writable because the worker constructs its ephemeral
// WireGuard and resolver files before dropping to the SOCKS5 user.
//
// When cfg.CellNetwork is set the cell joins that network + skips
// host port mapping: sibling containers reach it via docker DNS at
// containerName:CellSocksPort, no host loopback round-trip needed.
// When empty, host port mapping is used (smoke-test mode for direct
// host CLI access).
func (s *CellSpawner) buildHostConfig(
	confPath string,
) *container.HostConfig {
	initEnabled := true
	pidsLimit := int64(cellPIDsLimit)

	hostCfg := &container.HostConfig{
		AutoRemove: true,
		CapAdd: []string{
			cellCapabilityNetAdmin,
			cellCapabilitySetGID,
			cellCapabilitySetUID,
		},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
		Init:        &initEnabled,
		LogConfig: container.LogConfig{
			Type: "json-file",
			Config: map[string]string{
				"max-size": cellLogMaxSize,
				"max-file": cellLogMaxFiles,
			},
		},
		Sysctls: map[string]string{
			"net.ipv4.conf.all.src_valid_mark":   "1",
			"net.ipv6.conf.all.disable_ipv6":     "1",
			"net.ipv6.conf.default.disable_ipv6": "1",
		},
		Resources: container.Resources{
			Memory:    cellMemoryLimitBytes,
			NanoCPUs:  cellNanoCPUs,
			PidsLimit: &pidsLimit,
			Devices: []container.DeviceMapping{
				{
					PathOnHost:        "/dev/net/tun",
					PathInContainer:   "/dev/net/tun",
					CgroupPermissions: "rwm",
				},
			},
		},
		Binds: []string{
			confPath + ":/wgconf/wg0.conf:ro",
		},
	}

	if s.cfg.CellNetwork != "" {
		hostCfg.NetworkMode = container.NetworkMode(s.cfg.CellNetwork)

		return hostCfg
	}

	socksPort := mustParsePort(s.cfg.CellSocksPort)
	hostCfg.PortBindings = mobynet.PortMap{
		socksPort: []mobynet.PortBinding{
			{HostIP: loopbackBindAddr},
		},
	}

	return hostCfg
}

// buildNetworkingConfig declares the docker NetworkingConfig
// payload. When CellNetwork is set, the cell is created with a
// single endpoint on that network (no default-bridge attachment
// race). Empty CellNetwork → nil = docker default bridge.
func (s *CellSpawner) buildNetworkingConfig() *mobynet.NetworkingConfig {
	if s.cfg.CellNetwork == "" {
		return nil
	}

	return &mobynet.NetworkingConfig{
		EndpointsConfig: map[string]*mobynet.EndpointSettings{
			s.cfg.CellNetwork: {},
		},
	}
}

// Spawner-side polling + probe constants. Pulled into named
// consts so the magic-number linter stays happy + the values are
// visible to operators reading the file.
const (
	// waitReadyTickInterval is how often the spawner re-inspects
	// the container while waiting for the wireguard handshake.
	waitReadyTickInterval = 500 * time.Millisecond

	// probeSocks5Timeout is the per-probe budget against
	// the cell's SOCKS5 port.
	probeSocks5Timeout = 2 * time.Second

	// bestEffortKillTimeout caps the docker stop+remove issued
	// from the spawner's unhappy paths.
	bestEffortKillTimeout = 5 * time.Second
)

// waitReady polls until the cell's SOCKS5 listener accepts a TCP
// connect. Probe target depends on mode:
//
//   - sibling-network mode (cfg.CellNetwork set): probe
//     <containerName>:<CellSocksPort> via docker DNS.
//   - host-port-binding mode (CellNetwork empty): probe the host
//     port pulled from NetworkSettings.Ports[CellSocksPort].
//
// The cell starts microsocks only after its WireGuard handshake check passes,
// so a successful TCP connection makes the cell ready for SOCKS5 clients.
func (s *CellSpawner) waitReady(
	ctx context.Context, containerID, containerName string, req SpawnRequest,
) (*Tunnel, error) {
	ticker := time.NewTicker(waitReadyTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctxerrors.Wrapf(
				ErrSpawnTimeout,
				"container %s never reported running", containerID,
			)
		case <-ticker.C:
		}

		addr, err := s.resolveSocksAddr(ctx, containerID, containerName)
		if err != nil {
			continue
		}

		if addr == "" {
			continue
		}

		if !s.probeSocks5(ctx, addr) {
			continue
		}

		proxyURL, err := url.Parse(proxySchemeSOCKS5 + "://" + addr)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "parse proxy URL")
		}

		now := s.nowFn()

		exitCountry := req.ExitCountry
		if exitCountry == "" {
			exitCountry = exitCountryFromConf(req.ConfName)
		}

		return &Tunnel{
			ContainerID: containerID,
			ConfName:    req.ConfName,
			ProxyURL:    proxyURL,
			State:       TunnelStateHot,
			Pool:        req.Pool,
			ExitCountry: exitCountry,
			SpawnedAt:   now,
			HealthyAt:   now,
			LastUsedAt:  now,
		}, nil
	}
}

// resolveSocksAddr returns the host:port a probe + the returned
// ProxyURL should target, based on whether the cell is on a shared
// docker network (DNS-reachable hostname) or host-port-bound.
func (s *CellSpawner) resolveSocksAddr(
	ctx context.Context, containerID, containerName string,
) (string, error) {
	if s.cfg.CellNetwork != "" {
		return net.JoinHostPort(
			containerName, strconvItoa(s.cfg.CellSocksPort),
		), nil
	}

	info, err := s.docker.ContainerInspect(
		ctx, containerID, client.ContainerInspectOptions{},
	)
	if err != nil {
		return "", ctxerrors.Wrap(err, "inspect container")
	}

	if info.Container.NetworkSettings == nil {
		return "", ctxerrors.Wrap(
			ErrSpawnFailed, "no NetworkSettings on inspect response",
		)
	}

	socks := mustParsePort(s.cfg.CellSocksPort)

	bindings, ok := info.Container.NetworkSettings.Ports[socks]
	if !ok || len(bindings) == 0 {
		return "", nil
	}

	return net.JoinHostPort(
		bindings[0].HostIP.String(), bindings[0].HostPort,
	), nil
}

// strconvItoa avoids dragging strconv into this file just for one
// int-to-string conversion in the SOCKS5 addr build.
func strconvItoa(n int) string { return fmt.Sprintf("%d", n) }

// probeSocks5 reports whether the cell's SOCKS5 listener accepts a TCP
// connection. microsocks starts only after WireGuard setup, so this is a
// bounded readiness signal rather than an external egress assertion.
func (s *CellSpawner) probeSocks5(
	ctx context.Context, addr string,
) bool {
	probeCtx, cancel := context.WithTimeout(ctx, probeSocks5Timeout)
	defer cancel()

	d := net.Dialer{}

	conn, err := d.DialContext(probeCtx, "tcp", addr)
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

// bestEffortKill stops + removes a container, swallowing any
// errors. Used in unhappy paths of Spawn where the goal is "leave
// no orphan". It detaches cancellation from the request ctx: when Spawn fails
// on ctx-cancel (timeout, client gone), inheriting that dead ctx would cancel
// the kill immediately and leave Created containers behind. The detached
// context keeps scope values, so cleanup logs retain request correlation.
func (s *CellSpawner) bestEffortKill(
	ctx context.Context, containerID string,
) {
	killCtx, cancel := detachedCleanupContext(ctx)
	defer cancel()

	_ = s.Kill(killCtx, containerID)
}

func detachedCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(
		context.WithoutCancel(ctx), bestEffortKillTimeout,
	)
}

// buildContainerName produces a deterministic, collision-resistant
// container name so two simultaneous spawns for different pools
// don't clash.
func buildContainerName(pool, conf string, now time.Time) string {
	return fmt.Sprintf(
		"pr0xteus-tunnel-%s-%s-%d",
		sanitize(pool), sanitize(conf), now.UnixNano(),
	)
}

// sanitize replaces docker-name-illegal characters with '_'.
// Docker allows [a-zA-Z0-9][a-zA-Z0-9_.-]*; we keep the prefix
// conservative and only carry alphanumeric + '-' + '_' through.
func sanitize(in string) string {
	out := make([]byte, 0, len(in))

	for i := range len(in) {
		out = append(out, sanitizeByte(in[i]))
	}

	return string(out)
}

// sanitizeByte returns the input byte if docker-name-safe,
// otherwise '_'.
func sanitizeByte(c byte) byte {
	if isAlphaNum(c) || c == '-' || c == '_' {
		return c
	}

	return '_'
}

// isAlphaNum reports whether c is an ASCII letter or digit.
func isAlphaNum(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	}

	return false
}

// exitCountryFromConf retains the conventional "<cc>-<location>" filename
// fallback for configurations without explicit exit_countries metadata.
func exitCountryFromConf(conf string) string {
	if i := strings.IndexByte(conf, '-'); i > 0 {
		return strings.ToUpper(conf[:i])
	}

	return ""
}

// isNotFound returns true for docker's "no such container" error.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "No such container") ||
		strings.Contains(msg, "is not running")
}

// isRemovalInProgress reports whether a container-remove error is the benign
// AutoRemove race: cells run with AutoRemove enabled, so a stop can trigger
// docker's own removal, and an explicit remove issued right after may find one
// already underway. The container is going away either way, so Kill treats this
// as success rather than surfacing a 500 to an on-demand DELETE.
func isRemovalInProgress(err error) bool {
	if err == nil {
		return false
	}

	return strings.Contains(err.Error(), "is already in progress")
}

// mustParsePort builds a moby network.Port from an int port number;
// panic-on-malformed since the inputs come from the config struct
// which is validated at load.
func mustParsePort(port int) mobynet.Port {
	return mobynet.MustParsePort(fmt.Sprintf("%d/tcp", port))
}

// reapStateCreated is the docker container state for "create
// succeeded but start never happened" — the orphan class the
// bestEffortKill ctx bug was leaking. Reconciler always reaps
// these on sight.
const reapStateCreated = "created"

// orphanInFlightGrace is added to the spawn timeout to form the minimum age
// below which the ticker orphan-reaper leaves a container alone — it may be a
// spawn in progress that has not yet been published to a pool.
const orphanInFlightGrace = 30 * time.Second

// ReapOrphans lists every container tagged with LabelManaged and
// removes the ones that are either (a) in "created" state (failed
// spawn cleanup) or (b) running but not present in keepIDs (the
// set of container IDs the orchestrator still considers active —
// typically the pool's hot tunnels).
//
// Call at boot (keepIDs empty → kills everything from prior runs)
// + on a ticker (keepIDs = current pool state IDs → kills only
// containers that drifted out of orchestrator awareness).
//
// Returns the count of containers reaped.
func (s *CellSpawner) ReapOrphans(
	ctx context.Context, keepIDs map[string]struct{}, protectInFlight bool,
) (int, error) {
	listed, err := s.docker.ContainerList(ctx, client.ContainerListOptions{
		All: true,
		Filters: client.Filters{}.
			Add("label", LabelManaged+"=true").
			Add("label", LabelScope+"="+s.cfg.ManagedScope),
	})
	if err != nil {
		return 0, ctxerrors.Wrap(err, "list managed containers")
	}

	// A cell is "in flight" from ContainerStart until the manager publishes it
	// to a pool (setTunnel): running, but absent from keepIDs. The ticker path
	// (protectInFlight) therefore skips containers younger than the spawn
	// deadline + a buffer, so it never reaps a cell out from under an active
	// spawn. The boot path passes false — it must kill everything a prior
	// process left behind, regardless of age.
	minAge := s.cfg.SpawnTimeout + orphanInFlightGrace
	now := time.Now()

	reaped := 0

	for _, c := range listed.Items {
		if _, keep := keepIDs[c.ID]; keep {
			continue
		}

		if protectInFlight && now.Sub(time.Unix(c.Created, 0)) < minAge {
			continue
		}

		// Reap candidates: "created" (never started) + everything
		// else not on the keep list. Running + healthy but
		// orphaned = previous process owned it; the new process
		// can't proxy through a tunnel it doesn't track.
		state := string(c.State)
		if state != reapStateCreated && !shouldReapState(state) {
			continue
		}

		s.reapOne(ctx, c.ID)

		reaped++
	}

	return reaped, nil
}

// ListChildren discovers this controller's live cells directly from docker:
// every managed container carrying this controller's parent-id label, each with
// its control URL resolved from its current ephemeral IP on the cell network.
// Docker is the source of truth, so a cell that in-memory pool state lost track
// of still shows up here. When no parent id is known it falls back to the
// managed-scope label.
func (s *CellSpawner) ListChildren(ctx context.Context) ([]CellHandle, error) {
	filters := client.Filters{}.Add("label", LabelManaged+"=true")
	if s.cfg.ParentID != "" {
		filters = filters.Add("label", LabelParent+"="+s.cfg.ParentID)
	} else {
		filters = filters.Add("label", LabelScope+"="+s.cfg.ManagedScope)
	}

	listed, err := s.docker.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list child cells")
	}

	handles := make([]CellHandle, 0, len(listed.Items))
	for _, c := range listed.Items {
		handles = append(handles, CellHandle{
			ContainerID: c.ID,
			Pool:        c.Labels[LabelPool],
			ConfName:    c.Labels[LabelConf],
			State:       string(c.State),
			ControlURL:  s.controlURLFromSummary(c),
			CreatedAt:   time.Unix(c.Created, 0).UTC(),
		})
	}

	return handles, nil
}

// controlURLFromSummary resolves a cell's cellproxy control base URL from its
// current ephemeral IP on the cell network, or nil when there is no reachable
// control address (host-loopback smoke mode, or no endpoint on the network yet).
func (s *CellSpawner) controlURLFromSummary(c container.Summary) *url.URL {
	if s.cfg.CellNetwork == "" || c.NetworkSettings == nil {
		return nil
	}

	endpoint, ok := c.NetworkSettings.Networks[s.cfg.CellNetwork]
	if !ok || endpoint == nil || !endpoint.IPAddress.IsValid() {
		return nil
	}

	return &url.URL{
		Scheme: controlURLScheme,
		Host: net.JoinHostPort(
			endpoint.IPAddress.String(), strconvItoa(s.cfg.CellControlPort),
		),
	}
}

// reapOne kills a single container with its own short-lived
// detached context so a cancelled parent doesn't preempt the docker
// stop+remove (the original zombie-leak bug). It retains scope values for
// correlated diagnostics. Errors are swallowed because no caller remains to
// receive them.
func (s *CellSpawner) reapOne(ctx context.Context, containerID string) {
	killCtx, cancel := detachedCleanupContext(ctx)
	defer cancel()

	_ = s.Kill(killCtx, containerID)
}

// shouldReapState reports whether a container in the given docker
// state is eligible for orphan-reaping. Currently every non-removing
// state qualifies — the goal is to be aggressive about cleanup.
// "removing" is excluded because docker is already on it.
func shouldReapState(state string) bool {
	switch state {
	case "running", "exited", "dead", "paused", "restarting":
		return true
	}

	return false
}
