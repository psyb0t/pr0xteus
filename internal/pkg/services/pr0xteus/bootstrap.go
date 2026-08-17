package pr0xteus

import (
	"bufio"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	defaultControllerImage     = "psyb0t/pr0xteus:latest"
	developmentControllerImage = "psyb0t/pr0xteus:dev"

	bootstrapDirectoryMode = 0o700
	bootstrapFileMode      = 0o600
	bootstrapExampleMode   = 0o644
	randomTokenBytes       = 32

	dotEnvFileName        = ".env"
	dotEnvExampleFileName = ".env.example"
	composeFileName       = "docker-compose.yml"
	hostPortsFileName     = "docker-compose.host-ports.yml"
	noHostPortsFileName   = "docker-compose.no-host-ports.yml"
	apiTokenEnvName       = "PR0XTEUS_API_TOKEN" //nolint:gosec // G101 flags the env var NAME, not a secret value
	poolsRelativePath     = "secrets/pools.yaml"
	bundleRelativePath    = "secrets/wireguard"
	routingRelativePath   = "config/egress-routing.yaml"
	tailscaleStatePath    = "tailscale/state"

	configDirectoryLabel = "config directory"
)

//go:embed bootstrap/pools.yaml
var defaultPoolsYAML []byte

//go:embed bootstrap/egress-routing.yaml
var defaultRoutingYAML []byte

//go:embed bootstrap/docker-compose.yml
var defaultComposeYAML []byte

//go:embed bootstrap/docker-compose.host-ports.yml
var defaultHostPortsComposeYAML []byte

//go:embed bootstrap/docker-compose.no-host-ports.yml
var defaultNoHostPortsComposeYAML []byte

// BootstrapOptions defines where config init writes local operator material.
// HostConfigDir is the absolute host path Docker later bind-mounts into the
// controller at that same path for safe per-cell WireGuard file mounts.
type BootstrapOptions struct {
	ConfigDir       string
	HostConfigDir   string
	ControllerImage string
	Development     bool
}

// BootstrapResult describes writes without exposing the generated bearer token.
type BootstrapResult struct {
	Created   []string
	Preserved []string
	Refreshed []string
}

// ConfigCheckOptions defines the local configuration directory to validate.
type ConfigCheckOptions struct {
	ConfigDir string
}

// BootstrapConfig creates a starter configuration without replacing operator
// files. It deliberately leaves the WireGuard bundle empty because only the
// operator can supply a real provider or private-network config.
func BootstrapConfig(options BootstrapOptions) (BootstrapResult, error) {
	configDir, err := checkedAbsoluteDirectory(options.ConfigDir, "config directory")
	if err != nil {
		return BootstrapResult{}, err
	}

	hostConfigDir, err := checkedAbsolutePath(options.HostConfigDir, "host config directory")
	if err != nil {
		return BootstrapResult{}, err
	}

	controllerImage, err := checkedImageReference(options.ControllerImage)
	if err != nil {
		return BootstrapResult{}, err
	}

	if err := createBootstrapDirectories(configDir); err != nil {
		return BootstrapResult{}, err
	}

	result := BootstrapResult{}
	if err := writeBootstrapFiles(configDir, hostConfigDir, controllerImage, options.Development, &result); err != nil {
		return BootstrapResult{}, err
	}

	token, err := newAPIToken()
	if err != nil {
		return BootstrapResult{}, err
	}

	envPath := filepath.Join(configDir, dotEnvFileName)

	created, err := writeFileIfAbsent(
		envPath,
		[]byte(renderEnvFile(hostConfigDir, controllerImage, token, options.Development)),
	)
	if err != nil {
		return BootstrapResult{}, err
	}

	result.add(filepath.Join(hostConfigDir, dotEnvFileName), created)

	return result, nil
}

func createBootstrapDirectories(configDir string) error {
	for _, directory := range []string{
		filepath.Join(configDir, "secrets"),
		filepath.Join(configDir, bundleRelativePath),
		filepath.Join(configDir, "config"),
		filepath.Join(configDir, tailscaleStatePath),
	} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}

	return nil
}

func writeBootstrapFiles(
	configDir string,
	hostConfigDir string,
	controllerImage string,
	development bool,
	result *BootstrapResult,
) error {
	type bootstrapFile struct {
		path         string
		operatorPath string
		data         []byte
	}

	files := []bootstrapFile{
		{
			filepath.Join(configDir, poolsRelativePath),
			filepath.Join(hostConfigDir, poolsRelativePath),
			defaultPoolsYAML,
		},
		{
			filepath.Join(configDir, routingRelativePath),
			filepath.Join(hostConfigDir, routingRelativePath),
			defaultRoutingYAML,
		},
		{
			filepath.Join(configDir, composeFileName),
			filepath.Join(hostConfigDir, composeFileName),
			defaultComposeYAML,
		},
		{
			filepath.Join(configDir, hostPortsFileName),
			filepath.Join(hostConfigDir, hostPortsFileName),
			defaultHostPortsComposeYAML,
		},
		{
			filepath.Join(configDir, noHostPortsFileName),
			filepath.Join(hostConfigDir, noHostPortsFileName),
			defaultNoHostPortsComposeYAML,
		},
	}

	for _, file := range files {
		created, err := writeFileIfAbsent(file.path, file.data)
		if err != nil {
			return err
		}

		result.add(file.operatorPath, created)
	}

	examplePath := filepath.Join(configDir, dotEnvExampleFileName)
	if err := writeExampleFile(
		examplePath,
		[]byte(renderEnvExample(hostConfigDir, controllerImage, development)),
	); err != nil {
		return err
	}

	result.Refreshed = append(result.Refreshed, filepath.Join(hostConfigDir, dotEnvExampleFileName))

	return nil
}

// CheckConfig validates the bearer token, WireGuard bundle, pools, and routing
// policy consumed by the controller. It never starts Docker or mutates files.
func CheckConfig(options ConfigCheckOptions) error {
	configDir, err := checkedExistingDirectory(options.ConfigDir)
	if err != nil {
		return err
	}

	if _, err := loadAPITokenFromDotEnv(filepath.Join(configDir, dotEnvFileName)); err != nil {
		return ctxerrors.Wrap(err, "validate API token")
	}

	specs, knownPools, err := LoadPoolSpecs(
		filepath.Join(configDir, poolsRelativePath),
		filepath.Join(configDir, bundleRelativePath),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "validate pools")
	}

	if len(specs) == 0 {
		return ctxerrors.Wrap(ErrConfigInvalid, "pools.yaml has no pools")
	}

	if _, err := LoadRouter(
		filepath.Join(configDir, routingRelativePath),
		"",
		knownPools,
	); err != nil {
		return ctxerrors.Wrap(err, "validate egress routing")
	}

	return nil
}

func (result *BootstrapResult) add(path string, created bool) {
	if created {
		result.Created = append(result.Created, path)

		return
	}

	result.Preserved = append(result.Preserved, path)
}

func checkedAbsoluteDirectory(value, label string) (string, error) {
	path, err := checkedAbsolutePath(value, label)
	if err != nil {
		return "", err
	}

	if err := ensurePrivateDirectory(path); err != nil {
		return "", err
	}

	return path, nil
}

func checkedExistingDirectory(value string) (string, error) {
	path, err := checkedAbsolutePath(value, configDirectoryLabel)
	if err != nil {
		return "", err
	}

	info, err := os.Lstat(path)
	if err != nil {
		return "", ctxerrors.Wrapf(err, "stat %s", configDirectoryLabel)
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", ctxerrors.Wrapf(ErrConfigInvalid, "%s must be a real directory", configDirectoryLabel)
	}

	return path, nil
}

func checkedAbsolutePath(value, label string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" || !filepath.IsAbs(path) {
		return "", ctxerrors.Wrapf(ErrConfigInvalid, "%s must be an absolute path", label)
	}

	if strings.ContainsAny(path, "\r\n") {
		return "", ctxerrors.Wrapf(ErrConfigInvalid, "%s contains a newline", label)
	}

	return filepath.Clean(path), nil
}

func checkedImageReference(value string) (string, error) {
	image := strings.TrimSpace(value)
	if image == "" || strings.ContainsAny(image, " \t\r\n") {
		return "", ctxerrors.Wrap(ErrConfigInvalid, "controller image must be a non-empty image reference")
	}

	return image, nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, bootstrapDirectoryMode); err != nil {
			return ctxerrors.Wrap(err, "create config directory")
		}

		if err := os.Chmod(path, bootstrapDirectoryMode); err != nil {
			return ctxerrors.Wrap(err, "restrict config directory permissions")
		}

		return nil
	}

	if err != nil {
		return ctxerrors.Wrap(err, "stat config directory")
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ctxerrors.Wrapf(ErrConfigInvalid, "config path %q must be a real directory", path)
	}

	return nil
}

func writeFileIfAbsent(path string, contents []byte) (bool, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, ctxerrors.Wrapf(ErrConfigInvalid, "config path %q must be a regular file", path)
		}

		return false, nil
	}

	if !os.IsNotExist(err) {
		return false, ctxerrors.Wrap(err, "stat config file")
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, bootstrapFileMode)
	if err != nil {
		return false, ctxerrors.Wrap(err, "create config file")
	}

	if _, err := file.Write(contents); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return false, ctxerrors.Wrap(closeErr, "close partially written config file")
		}

		return false, ctxerrors.Wrap(err, "write config file")
	}

	if err := file.Close(); err != nil {
		return false, ctxerrors.Wrap(err, "close config file")
	}

	return true, nil
}

func writeExampleFile(path string, contents []byte) error {
	if err := validateExamplePath(path); err != nil {
		return err
	}

	temporaryFile, err := os.CreateTemp(filepath.Dir(path), ".env.example-*")
	if err != nil {
		return ctxerrors.Wrap(err, "create config example")
	}

	temporaryPath := temporaryFile.Name()

	defer func() {
		_ = os.Remove(temporaryPath) // best-effort cleanup after a failed replacement.
	}()

	if err := temporaryFile.Chmod(bootstrapExampleMode); err != nil {
		_ = temporaryFile.Close()

		return ctxerrors.Wrap(err, "set config example permissions")
	}

	if _, err := temporaryFile.Write(contents); err != nil {
		_ = temporaryFile.Close()

		return ctxerrors.Wrap(err, "write config example")
	}

	if err := temporaryFile.Close(); err != nil {
		return ctxerrors.Wrap(err, "close config example")
	}

	if err := os.Rename(temporaryPath, path); err != nil {
		return ctxerrors.Wrap(err, "replace config example")
	}

	return nil
}

func validateExamplePath(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return ctxerrors.Wrap(err, "stat config example")
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ctxerrors.Wrapf(ErrConfigInvalid, "config path %q must be a regular file", path)
	}

	return nil
}

func newAPIToken() (string, error) {
	token := make([]byte, randomTokenBytes)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return "", ctxerrors.Wrap(err, "generate API token")
	}

	return hex.EncodeToString(token), nil
}

func loadAPITokenFromDotEnv(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open .env")
	}

	defer func() {
		_ = file.Close() // read-only close cannot change the validation result.
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) != apiTokenEnvName {
			continue
		}

		return ValidateAPIToken(strings.Trim(value, "\"'"))
	}

	if err := scanner.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "read .env")
	}

	return nil, ctxerrors.Wrapf(ErrConfigInvalid, "%s is required in .env", apiTokenEnvName)
}

func renderEnvFile(
	hostConfigDir string,
	controllerImage string,
	apiToken string,
	development bool,
) string {
	if development {
		controllerImage = developmentControllerImage
	}

	lines := []string{
		"# Generated by pr0xteus config init. Keep this file local.",
		"PR0XTEUS_CONFIG_DIR=" + hostConfigDir,
		"PR0XTEUS_CONTROLLER_IMAGE=" + controllerImage,
		apiTokenEnvName + "=" + apiToken,
		"PR0XTEUS_HTTP_HOST_PORT=127.0.0.1:8000",
		"PR0XTEUS_METRICS_HOST_PORT=127.0.0.1:9091",
		"PR0XTEUS_SOCKS_HOST_PORT=127.0.0.1:1080",
		"PR0XTEUS_SOCKS_PUBLIC_ADDRESS=127.0.0.1:1080",
		"PR0XTEUS_DISABLE_HOST_PORTS=false",
		"PR0XTEUS_TAILSCALE_ENABLED=false",
		"TS_AUTHKEY=",
		"TS_HOSTNAME=pr0xteus",
		"TS_EXTRA_ARGS=--accept-dns=false",
		"LOG_LEVEL=info",
		"LOG_FORMAT=json",
		"LOG_ADD_SOURCE=true",
	}

	return strings.Join(lines, "\n") + "\n"
}

func renderEnvExample(hostConfigDir string, controllerImage string, development bool) string {
	if development {
		controllerImage = developmentControllerImage
	}

	lines := []string{
		"# This file is refreshed by pr0xteus setup and upgrade. Copy values into .env; .env is never overwritten.",
		"# Never commit .env, WireGuard bundles, routing policy, or the API token.",
		"",
		"# Docker binds this absolute host directory into the controller at the same path.",
		"PR0XTEUS_CONFIG_DIR=" + hostConfigDir,
		"",
		"# Required private controller bearer token. config init generates a random owner-only value in .env.",
		apiTokenEnvName + "=REPLACE_ME",
		"",
		"# Published controller image. setup and upgrade pin this automatically to a release.",
		"PR0XTEUS_CONTROLLER_IMAGE=" + controllerImage,
		"",
		"# Host-port bindings. Each value is HOST:PORT; defaults stay on host loopback.",
		"# Use a real protected host address when changing SOCKS_HOST_PORT: 0.0.0.0 is",
		"# valid for binding every interface but is not an address a client can use.",
		"PR0XTEUS_HTTP_HOST_PORT=127.0.0.1:8000",
		"PR0XTEUS_METRICS_HOST_PORT=127.0.0.1:9091",
		"PR0XTEUS_SOCKS_HOST_PORT=127.0.0.1:1080",
		"",
		"# Address returned in allocated SOCKS URLs. Keep it aligned with the reachable",
		"# SOCKS host port; do not use 0.0.0.0 here.",
		"PR0XTEUS_SOCKS_PUBLIC_ADDRESS=127.0.0.1:1080",
		"",
		"# Set true to remove every pr0xteus host-port binding. Use this for the",
		"# Tailscale sidecar or another private Docker-network gateway.",
		"PR0XTEUS_DISABLE_HOST_PORTS=false",
		"",
		"# Optional tailnet-only API. Set true and provide a key from your own tailnet.",
		"PR0XTEUS_TAILSCALE_ENABLED=false",
		"TS_AUTHKEY=your-tailscale-auth-key",
		"TS_HOSTNAME=pr0xteus",
		"TS_EXTRA_ARGS=--accept-dns=false",
		"",
		"LOG_LEVEL=info",
		"LOG_FORMAT=json",
		"LOG_ADD_SOURCE=true",
	}

	return strings.Join(lines, "\n") + "\n"
}
