# Documentation

Prefix: DOC. `docs/` is the documentation site, built with Docusaurus from `website/` and published to GitHub Pages from `main` at https://psyb0t.github.io/pr0xteus/.

- **DOC1:** A new page goes into `website/sidebars.mjs`, where nesting replaces the old `parent` field, and into `website/static/llms.txt`.
- **DOC3:** The README H1, `title` in `website/docusaurus.config.mjs`, the H1 of `docs/index.md` and the H1 of `website/static/llms.txt` are the same name, `pr0xteus`. Change them together.
- **DOC4:** Pages are plain Markdown, not MDX. Do not add Liquid tags or `raw` comments.
- **DOC5:** `make docs` builds the site into `website/build` and fails on a broken link or anchor. It must pass before handoff.
