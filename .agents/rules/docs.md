# Documentation

Prefix: DOC. `docs/` is the documentation site, built by GitHub Pages from `main` with Jekyll and the just-the-docs theme, published at https://psyb0t.github.io/pr0xteus/.

- **DOC1:** Every page has front matter with a unique `title` and a `description` of 50 to 170 characters. Child pages set `parent` to the exact parent title.
- **DOC2:** Page-to-page links use the relative `.md` path. Links to files outside `docs/` use the full `https://github.com/psyb0t/pr0xteus/blob/main/...` URL (`tree/main/...` for a directory). The README links to the site with full `https://psyb0t.github.io/pr0xteus/...` URLs.
- **DOC3:** The README H1, `title` in `docs/_config.yml`, the H1 of `docs/index.md` and the H1 of `docs/llms.txt` are the same name, `pr0xteus`. Change them together.
- **DOC4:** Wrap code blocks that contain `{{` or `{%` in `{% raw %}` comments, or Jekyll renders them as nothing.
- **DOC5:** A behavior, API, configuration or deployment change updates its page in the same change. `make docs` builds the site into `.testing/docs-site`; run the link checker on the output before handing off.
- **DOC6:** Prose paragraphs stay on one source line. No em dashes, "directory" rather than "folder", and examples that run as written.
