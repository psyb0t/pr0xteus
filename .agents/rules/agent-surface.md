# Agent skill and plugin manifests

Prefix: AGT. The skill and manifests under `.agents/` describe pr0xteus to other agents and are published (ClawHub on tags, the psyb0t plugin marketplace).

- **AGT1:** The skill is `.agents/skills/pr0xteus/SKILL.md` with `references/setup.md`. A route, field, install or wrapper change updates both in the same change.
- **AGT2:** pr0xteus has no MCP server. The skill is documentation only; do not add a bridge plugin, hook, `.mcp.json` or executable code under `.agents/`.
- **AGT3:** The Codex manifest `version` in `.agents/.codex-plugin/plugin.json` moves with the release tag in the release commit. The Claude manifest (`.agents/.claude-plugin/plugin.json`) carries no version.
- **AGT4:** Examples use placeholder tokens, hosts and lease URLs. No real token, WireGuard file or lease URL goes into any file under `.agents/`.
