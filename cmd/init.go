package main

import pr0xteus "github.com/psyb0t/pr0xteus/internal/pkg/services/pr0xteus"

//nolint:gochecknoinits // Project startup hook pairs controller and cell images before services initialize.
func init() {
	configurePr0xteusCellImage(buildVersion)
}

func configurePr0xteusCellImage(version string) {
	pr0xteus.ConfigureCellImageVersion(version)
}
