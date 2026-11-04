# Agent integrations

The documentation skill covers controller setup, the private HTTP API and proxy leases. Give it a trusted controller URL and API token.

## Claude Code

```bash
claude plugin marketplace add psyb0t/agents
claude plugin install pr0xteus@psyb0t
```

Claude Code asks for the private controller URL and bearer token when the plugin is enabled; the token is stored as sensitive user configuration.

## Codex

```bash
codex plugin marketplace add psyb0t/agents
codex plugin add pr0xteus@psyb0t
```

Inside this repository, use `$pr0xteus`. After marketplace installation, use `$pr0xteus:pr0xteus`.

## OpenClaw

The same documentation skill is published to ClawHub on tagged releases:

```bash
openclaw skills install @psyb0t/pr0xteus
```

Read the [skill setup reference](../.agents/skills/pr0xteus/references/setup.md) for configuration and usage.

Return to the [README](../README.md).
