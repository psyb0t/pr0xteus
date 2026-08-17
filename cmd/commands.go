package main

import (
	"github.com/psyb0t/ctxerrors"
	pr0xteus "github.com/psyb0t/pr0xteus/internal/pkg/services/pr0xteus"
	"github.com/spf13/cobra"
)

func commands() []*cobra.Command {
	return []*cobra.Command{buildConfigCommand()}
}

func buildConfigCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Create or validate local deployment configuration",
	}

	command.AddCommand(
		buildConfigInitCommand(),
		buildConfigCheckCommand(),
	)

	return command
}

func buildConfigInitCommand() *cobra.Command {
	options := pr0xteus.BootstrapOptions{}

	command := &cobra.Command{
		Use:   "init",
		Short: "Create missing local configuration without overwriting operator files",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return initializeConfig(command, options)
		},
	}

	configureConfigInitFlags(command, &options)

	return command
}

func initializeConfig(command *cobra.Command, options pr0xteus.BootstrapOptions) error {
	result, err := pr0xteus.BootstrapConfig(options)
	if err != nil {
		return ctxerrors.Wrap(err, "initialize pr0xteus config")
	}

	for _, path := range result.Created {
		command.Printf("created %s\n", path)
	}

	for _, path := range result.Preserved {
		command.Printf("preserved %s\n", path)
	}

	for _, path := range result.Refreshed {
		command.Printf("refreshed %s\n", path)
	}

	command.Println("add an authorized WireGuard .conf, update pools.yaml, then run config check")

	return nil
}

func configureConfigInitFlags(command *cobra.Command, options *pr0xteus.BootstrapOptions) {
	command.Flags().StringVar(
		&options.ConfigDir,
		"config-dir",
		"/config",
		"configuration directory mounted into this command",
	)
	command.Flags().StringVar(
		&options.HostConfigDir,
		"host-config-dir",
		"",
		"absolute host path Docker will mount into the controller",
	)
	command.Flags().StringVar(
		&options.ControllerImage,
		"controller-image",
		"psyb0t/pr0xteus:latest",
		"published controller image to write into .env",
	)
	command.Flags().StringVar(
		&options.RuntimeUser,
		"runtime-user",
		"",
		"UID:GID used by the controller to read the host-mounted operator config",
	)
	command.Flags().BoolVar(
		&options.Development,
		"development",
		false,
		"write local image tags for source development",
	)
	command.Flags().BoolVar(
		&options.RefreshRuntimeTemplates,
		"refresh-runtime-templates",
		false,
		"refresh generated Docker Compose templates without changing operator config",
	)
}

func buildConfigCheckCommand() *cobra.Command {
	options := pr0xteus.ConfigCheckOptions{}

	command := &cobra.Command{
		Use:   "check",
		Short: "Validate local token, WireGuard bundle, pools, and routing",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := pr0xteus.CheckConfig(options); err != nil {
				return ctxerrors.Wrap(err, "check pr0xteus config")
			}

			command.Println("configuration is valid")

			return nil
		},
	}

	command.Flags().StringVar(
		&options.ConfigDir,
		"config-dir",
		"/config",
		"configuration directory mounted into this command",
	)

	return command
}
