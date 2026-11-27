const group = (id, label, items) => ({
  type: "category",
  label,
  link: { type: "doc", id },
  items,
});

export default {
  docs: [
    "index",
    "complete-example",
    group("deploy", "Installation and upgrades", ["configuration", "manual-deployment"]),
    "api",
    "go-client",
    "integrations",
    "monitoring",
    "troubleshooting",
    "security",
    group("controller", "Controller internals", ["cell"]),
    group("development", "Development", ["architecture", "getting-started", "services-and-lifecycle", "framework-updates"]),
  ],
};
