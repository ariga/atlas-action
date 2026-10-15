// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

//go:build manifest
// +build manifest

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"ariga.io/atlas-action/atlasaction"
	"github.com/spf13/cobra"
)

// root represents the root command when called without any subcommands.
var root = &cobra.Command{
	Use:          "gen",
	Short:        "Generate manifest files for Atlas GitHub Actions",
	SilenceUsage: true,
}

func main() {
	root.SetOut(os.Stdout)
	root.AddCommand(
		githubManifestCmd(),
		gitlabTemplateCmd(),
		orbTemplateCmd(),
		teamcityTemplateCmd(),
		azureTaskCmd(),
		markdownDocCmd(),
	)
	err := root.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func githubManifestCmd() *cobra.Command {
	var (
		flags struct {
			OutputDir string
		}
		cmd = &cobra.Command{
			Use:   "github-manifest",
			Short: "Generate GitHub Actions manifest files from a manifest",
			RunE: func(cmd *cobra.Command, args []string) error {
				actions, err := atlasaction.ParseManifest()
				if err != nil {
					return err
				}
				return actions.GitHubManifests(flags.OutputDir)
			},
		}
	)
	cmd.Flags().StringVarP(&flags.OutputDir, "output-dir", "o", ".", "The output directory for the generated files")
	return cmd
}

func gitlabTemplateCmd() *cobra.Command {
	var (
		flags struct {
			OutputDir, Version string
			Checksums          map[string]string
		}
		cmd = &cobra.Command{
			Use:   "gitlab-template",
			Short: "Generate GitLab template files from a manifest",
			RunE: func(cmd *cobra.Command, args []string) error {
				// The templates verify the downloaded binary against these,
				// so refuse to generate templates without them.
				for _, p := range []string{"linux-amd64", "linux-arm64"} {
					if b, err := hex.DecodeString(flags.Checksums[p]); err != nil || len(b) != sha256.Size {
						return fmt.Errorf("missing or invalid --sha256 for %s", p)
					}
				}
				actions, err := atlasaction.ParseManifest()
				if err != nil {
					return err
				}
				ver := strings.TrimPrefix(flags.Version, "v")
				for actionIdx := range actions.Actions {
					actions.Actions[actionIdx].Version = ver
					actions.Actions[actionIdx].Checksums = flags.Checksums
				}
				return actions.GitLabTemplates(flags.OutputDir)
			},
		}
	)
	cmd.Flags().StringVarP(&flags.OutputDir, "output-dir", "o", ".", "The output directory for the generated files")
	cmd.Flags().StringVarP(&flags.Version, "version", "v", "v1.0.0", "The version of the TeamCity recipes")
	cmd.Flags().StringToStringVar(&flags.Checksums, "sha256", nil, "The SHA256 of the atlas-action binary per platform, e.g. linux-amd64=<hex>")
	return cmd
}

func teamcityTemplateCmd() *cobra.Command {
	var (
		flags struct {
			OutputDir string
			Version   string
		}
		cmd = &cobra.Command{
			Use:   "teamcity-template",
			Short: "Generate TeamCity recipe files from a manifest",
			RunE: func(cmd *cobra.Command, args []string) error {
				actions, err := atlasaction.ParseManifest()
				if err != nil {
					return err
				}
				ver := strings.TrimPrefix(flags.Version, "v")
				for actionIdx := range actions.Actions {
					actions.Actions[actionIdx].Version = ver
				}
				return actions.TeamCityTemplates(flags.OutputDir)
			},
		}
	)
	cmd.Flags().StringVarP(&flags.OutputDir, "output-dir", "o", ".", "The output directory for the generated files")
	cmd.Flags().StringVarP(&flags.Version, "version", "v", "v1.0.0", "The version of the TeamCity recipes")
	return cmd
}

func orbTemplateCmd() *cobra.Command {
	var (
		flags struct {
			OutputDir string
			Version   string
		}
		cmd = &cobra.Command{
			Use:   "orb-template",
			Short: "Generate CircleCI orb command files from a manifest",
			RunE: func(cmd *cobra.Command, args []string) error {
				actions, err := atlasaction.ParseManifest()
				if err != nil {
					return err
				}
				ver := strings.TrimPrefix(flags.Version, "v")
				for actionIdx := range actions.Actions {
					actions.Actions[actionIdx].Version = ver
				}
				return actions.OrbTemplates(flags.OutputDir)
			},
		}
	)
	cmd.Flags().StringVarP(&flags.OutputDir, "output-dir", "o", ".", "The output directory for the generated files")
	cmd.Flags().StringVarP(&flags.Version, "version", "v", "v1.0.0", "The version of the Atlas Action used by the orb")
	return cmd
}

func azureTaskCmd() *cobra.Command {
	var (
		flags struct {
			TaskJSON string
		}
		cmd = &cobra.Command{
			Use:   "azure-task",
			Short: "Generate Azure DevOps task JSON",
			RunE: func(cmd *cobra.Command, args []string) error {
				actions, err := atlasaction.ParseManifest()
				if err != nil {
					return err
				}
				var w io.Writer
				if flags.TaskJSON == "" {
					w = os.Stdout
				} else {
					t, err := os.OpenFile(flags.TaskJSON, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
					if err != nil {
						return fmt.Errorf("opening file %s: %w", flags.TaskJSON, err)
					}
					defer t.Close()
					w = t
				}
				return actions.AzureTaskJSON(w)
			},
		}
	)
	cmd.Flags().StringVarP(&flags.TaskJSON, "task-json", "t", "", "The output file for the Azure DevOps task JSON")
	return cmd
}

func markdownDocCmd() *cobra.Command {
	var (
		flags struct {
			OutputDir string
			Readme    string
		}
		cmd = &cobra.Command{
			Use:   "docs",
			Short: "Generate Azure DevOps task JSON",
			RunE: func(cmd *cobra.Command, args []string) error {
				actions, err := atlasaction.ParseManifest()
				if err != nil {
					return err
				}
				if err := actions.MarkdownDocs(flags.OutputDir); err != nil {
					return err
				}
				if flags.Readme != "" {
					if err := actions.MarkdownREADME(flags.Readme); err != nil {
						return err
					}
				}
				return nil
			},
		}
	)
	cmd.Flags().StringVarP(&flags.OutputDir, "output-dir", "o", ".", "The output directory for the generated files")
	cmd.Flags().StringVar(&flags.Readme, "readme", "", "The output file for the generated README")
	return cmd
}
