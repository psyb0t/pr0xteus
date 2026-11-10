# Testing

Prefix: TEST. How to run each suite is in `AGENTS.md` and `docs/development.md`.

- **TEST1:** Unit tests sit beside the code and use testify (`assert`, `require`). Controller tests fake Docker through the `Spawner` and Docker client interfaces; they do not start real cells.
- **TEST2:** Behavior that needs real containers (the production image, WireGuard handshakes, proxy egress, every HTTP route) belongs in `tests/controlapi/` behind the `integration` build tag, driven by `tests/testinfra`. It uses the local WireGuard peer in `tests/testinfra/wireguard/`, never a real provider.
- **TEST3:** `tests/real/` (`integration && real` tags) is the only suite that uses a real provider. It is opt-in through `make test-real` and never runs in CI.
- **TEST4:** CI tests must need no secrets and no running stack. A test that needs `make restart` or the installer stack is local-only, like `make test-installed`.
- **TEST5:** The coverage floor is 90% (`MIN_TEST_COVERAGE`). Raise coverage for new code rather than lowering the floor or excluding files.
- **TEST6:** Framework tests (`internal/pkg/service-manager`, `pkg/runner`) reset singleton state and drive fake services and parent-context cancellation, not host signals or the generated `services.Init()`.
