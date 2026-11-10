# Documentation

Prefix: DOC. `docs/` is the documentation site, built by GitHub Pages from `main` with Jekyll and the just-the-docs theme, published at https://psyb0t.github.io/pr0xteus/.

- **DOC1:** Child pages set `parent` to the exact parent title.
- **DOC3:** The README H1, `title` in `docs/_config.yml`, the H1 of `docs/index.md` and the H1 of `docs/llms.txt` are the same name, `pr0xteus`. Change them together.
- **DOC4:** Wrap code blocks that contain `{{` or `{%` in `{% raw %}` comments, or Jekyll renders them as nothing.
- **DOC5:** `make docs` builds the site into `.testing/docs-site`; run the link checker on the output before handing off.
