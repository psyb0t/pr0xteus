# Cell image

The cell is a disposable worker: one WireGuard configuration in, one private SOCKS5 listener out. `entrypoint.sh` brings up the tunnel behind a default-drop firewall and starts `cellproxy` as UID 1500 once a handshake arrives.

The boot sequence, privilege model and image build are documented in [cell image](https://psyb0t.github.io/pr0xteus/cell/).
