package atlasaction

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/sethvargo/go-githubactions"
)

func Version(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	if input := act.GetInput("test-input"); input != "" {
		act.Infof("test-input: %s", input)
	}
	version, err := client.Version(ctx)
	if err != nil {
		return err
	}
	act.SetOutput("version", version.Version)
	return nil
}

var (
	//go:embed atlas.hcl.tmpl
	tmpl   string
	config = template.Must(template.New("atlashcl").Parse(tmpl))
)

// MigrateApply runs the GitHub Action for "ariga/atlas-action/migrate/apply".
func MigrateApply(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	input, err := migApplyInput(act)
	if err != nil {
		return err
	}
	run, err := migrateApply(ctx, client, input)
	if err != nil {
		return err
	}
	act.SetOutput("error", run.Error)
	act.SetOutput("current", run.Current)
	act.SetOutput("target", run.Target)
	act.SetOutput("pending_count", strconv.Itoa(len(run.Pending)))
	act.SetOutput("applied_count", strconv.Itoa(len(run.Applied)))
	act.Infof("Run complete: +%v", run)
	return nil
}

type (
	// migrateApplyInput is created from the GitHub Action "with" configuration.
	migrateApplyInput struct {
		// URL is the target database URL.
		URL string
		// Amount is the number of migrations to apply.
		Amount uint64
		// TxMode is the Atlas transaction mode to use.
		TxMode string
		// Baseline is the baseline version to use.
		Baseline string
		// AllowDirty instructs Atlas whether to allow a dirty target database.
		AllowDirty bool
		// Dir is the directory to use for migrations. Atlas expects this to be a URL
		// such as file://path/to/migrations.
		Dir string
		// RevisionsSchema is the schema to use for the revisions table.
		RevisionsSchema string

		// Cloud is the Atlas Cloud configuration.
		Cloud cloud
	}
	cloud struct {
		// The target directory slug.
		Dir string
		// The tag for the migration directory revision to use.
		Tag string
		// The Atlas Cloud endpoint URL.
		URL string
	}
)

// migApplyInput loads the input from the GitHub Action configuration.
func migApplyInput(act *githubactions.Action) (*migrateApplyInput, error) {
	i := &migrateApplyInput{
		URL: act.GetInput("url"),
	}
	if i.URL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if txm := act.GetInput("tx-mode"); txm != "" {
		switch txm {
		case "all", "none", "file":
			i.TxMode = txm
		default:
			return nil, fmt.Errorf("invalid tx-mode %q", txm)
		}
		i.TxMode = act.GetInput("tx-mode")
	}
	i.Baseline = act.GetInput("baseline")
	if ad := act.GetInput("allow-dirty"); ad != "" {
		allowDirty, err := strconv.ParseBool(strings.ToLower(ad))
		if err != nil {
			return nil, fmt.Errorf("invalid allow-dirty %q", ad)
		}
		i.AllowDirty = allowDirty
	}
	i.Dir = act.GetInput("dir")
	i.Cloud.Dir = act.GetInput("dir-name")
	if i.Dir != "" && i.Cloud.Dir != "" {
		return nil, fmt.Errorf("dir and dir-name are mutually exclusive")
	}
	i.Cloud.URL = act.GetInput("cloud-url")
	i.Cloud.Tag = act.GetInput("tag")
	return i, nil
}

// migrateApply runs the "migrate apply" for the input.
func migrateApply(ctx context.Context, client *atlasexec.Client, i *migrateApplyInput) (*atlasexec.ApplyReport, error) {
	params := &atlasexec.MigrateApplyParams{
		URL:             i.URL,
		Amount:          i.Amount,
		TxMode:          i.TxMode,
		BaselineVersion: i.Baseline,
	}
	if i.Dir != "" {
		params.DirURL = i.Dir // We let Atlas verify that its an actual URL.
	}
	if i.Cloud.Dir != "" {
		var buf bytes.Buffer
		if err := config.Execute(&buf, i); err != nil {
			return nil, err
		}
		cfg, clean, err := atlasexec.TempFile(buf.String(), "hcl")
		if err != nil {
			return nil, err
		}
		// nolint:errcheck
		defer clean()
		params.ConfigURL = cfg
		params.Env = "atlas"
	}
	return client.MigrateApply(ctx, params)
}
