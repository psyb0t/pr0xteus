const repository = "https://github.com/psyb0t/pr0xteus";
const goReference = "https://pkg.go.dev/github.com/psyb0t/pr0xteus/pkg/client";
const baseUrl = "/pr0xteus/";
const docsPath = "../docs";

export default {
  title: "pr0xteus",
  tagline: "Private, on-demand WireGuard exits as short-lived proxy leases",
  url: "https://psyb0t.github.io",
  baseUrl,
  trailingSlash: true,
  future: { faster: { rspackBundler: true, swcJsLoader: true } },
  organizationName: "psyb0t",
  projectName: "pr0xteus",
  onBrokenLinks: "throw",
  onBrokenAnchors: "throw",
  markdown: { format: "detect", hooks: { onBrokenMarkdownLinks: "throw" } },
  scripts: [{ src: "https://psyb0t.github.io/analytics.js", async: true }],
  presets: [
    ["classic", {
      blog: false,
      sitemap: { ignorePatterns: [baseUrl + "search", baseUrl + "search/"] },
      theme: { customCss: "./src/css/theme.css" },
      docs: {
        path: docsPath,
        routeBasePath: "/",
        sidebarPath: "./sidebars.mjs",
        editUrl: ({ docPath }) => repository + "/edit/main/docs/" + docPath,
      },
    }],
  ],
  themes: [
    ["@easyops-cn/docusaurus-search-local", {
      docsDir: docsPath,
      docsRouteBasePath: "/",
      indexBlog: false,
      hashed: true,
    }],
  ],
  themeConfig: {
    navbar: {
      items: [
        { href: "https://psyb0t.github.io/projects/", label: "All projects", position: "left" },
        { href: goReference, label: "Go client reference", position: "right" },
        { href: repository + "/blob/main/CHANGELOG.md", label: "Changelog", position: "right" },
        { href: repository, label: "GitHub", position: "right" },
      ],
    },
    colorMode: { defaultMode: "dark" },
  },
};
