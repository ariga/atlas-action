package main

import (
	"os"

	"ariga.io/atlas-action/atlasaction/gen"
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
			Manifest  string
			OutputDir string
		}
		cmd = &cobra.Command{
			Use:   "github-manifest",
			Short: "Generate GitHub Actions manifest files from a manifest",
			RunE: func(cmd *cobra.Command, args []string) error {
				f, err := os.Open(flags.Manifest)
				if err != nil {
					return err
				}
				defer f.Close()
				actions, err := gen.ParseManifest(f)
				if err != nil {
					return err
				}
				return actions.GitHubManifests(flags.OutputDir)
			},
		}
	)
	cmd.Flags().StringVarP(&flags.Manifest, "manifest", "m", "atlas-actions.yml", "The manifest file to generate the output from")
	cmd.Flags().StringVarP(&flags.OutputDir, "output-dir", "o", ".", "The output directory for the generated files")
	return cmd
}

func azureTaskCmd() *cobra.Command {
	var (
		flags struct {
			Manifest string
			TaskJSON string
		}
		cmd = &cobra.Command{
			Use:   "azure-task",
			Short: "Generate Azure DevOps task JSON",
			RunE: func(cmd *cobra.Command, args []string) error {
				f, err := os.Open(flags.Manifest)
				if err != nil {
					return err
				}
				defer f.Close()
				actions, err := gen.ParseManifest(f)
				if err != nil {
					return err
				}
				return actions.AzureTaskJSON(flags.TaskJSON)
			},
		}
	)
	cmd.Flags().StringVarP(&flags.Manifest, "manifest", "m", "atlas-actions.yml", "The manifest file to generate the output from")
	cmd.Flags().StringVarP(&flags.TaskJSON, "task-json", "t", "task.json", "The output file for the Azure DevOps task JSON")
	return cmd
}

func markdownDocCmd() *cobra.Command {
	var (
		flags struct {
			Manifest  string
			OutputDir string
		}
		cmd = &cobra.Command{
			Use:   "docs",
			Short: "Generate Azure DevOps task JSON",
			RunE: func(cmd *cobra.Command, args []string) error {
				f, err := os.Open(flags.Manifest)
				if err != nil {
					return err
				}
				defer f.Close()
				actions, err := gen.ParseManifest(f)
				if err != nil {
					return err
				}
				return actions.MarkdownDocs(flags.OutputDir)
			},
		}
	)
	cmd.Flags().StringVarP(&flags.Manifest, "manifest", "m", "atlas-actions.yml", "The manifest file to generate the output from")
	cmd.Flags().StringVarP(&flags.OutputDir, "output-dir", "o", ".", "The output directory for the generated files")
	return cmd
}
