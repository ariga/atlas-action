package atlasaction

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/sethvargo/go-githubactions"
)

// MigrateApply runs the GitHub Action for "ariga/atlas-action/migrate/apply".
func MigrateApply(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	params := &atlasexec.MigrateApplyParams{
		URL:             act.GetInput("url"),
		DirURL:          act.GetInput("dir"),
		TxMode:          act.GetInput("tx-mode"),  // Hidden param.
		BaselineVersion: act.GetInput("baseline"), // Hidden param.
	}
	run, err := client.MigrateApply(ctx, params)
	if err != nil {
		act.SetOutput("error", err.Error())
		return err
	}
	if run.Error != "" {
		act.SetOutput("error", run.Error)
		return fmt.Errorf("run failed: %s", run.Error)
	}
	act.SetOutput("current", run.Current)
	act.SetOutput("target", run.Target)
	act.SetOutput("pending_count", strconv.Itoa(len(run.Pending)))
	act.SetOutput("applied_count", strconv.Itoa(len(run.Applied)))
	act.Infof("Run complete: +%v", run)
	return nil
}
