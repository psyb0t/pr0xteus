# Operator configuration

All sensitive or provider-specific material lives under your config directory (`~/.config/pr0xteus/` per-user, `/etc/pr0xteus/` system-wide), stays out of Git, and stays out of Docker build contexts:

- `secrets/wireguard/*.conf`: real WireGuard files.
- `secrets/pools.yaml`: named pools and their approved config basenames.
- `config/egress-routing.yaml`: country-to-pool policy.
- `.env`: the private bearer token, absolute host configuration path, and controller image selection.

The installer writes `.env`; it is not something you need to create by hand. The controller chooses its cell itself: `latest` pulls `cell-latest`, and `vX.Y.Z` pulls `cell-vX.Y.Z` before allocating a tunnel. That pairing is baked into the controller binary and cannot be changed through environment settings. Local development builds both images with the `dev` version, so it uses `psyb0t/pr0xteus:cell-dev` automatically.

To stay on a particular release, change `PR0XTEUS_CONTROLLER_IMAGE=psyb0t/pr0xteus:vX.Y.Z` in `~/.config/pr0xteus/.env`, run `pr0xteus setup`, then run `pr0xteus start`.

Pool filenames need not be provider-specific. For a file that does not follow the legacy `<country>-<location>.conf` convention, add its country explicitly:

```yaml
pools:
  primary:
    configs: [my-provider-node]
    exit_countries:
      my-provider-node: US
```

See [deployment](deploy.md) for installer paths, upgrades, ports and tailnet access. Return to the [quick start](../README.md#quick-start).
