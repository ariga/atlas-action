// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	_ "embed"
	"errors"
	"strconv"
	"strings"
	"text/template"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/sethvargo/go-githubactions"
)

var (
	//go:embed atlas.hcl.tmpl
	tmpl   string
	config = template.Must(template.New("atlashcl").Parse(tmpl))
)

// MigrateApply runs the GitHub Action for "ariga/atlas-action/migrate/apply".
func MigrateApply(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	params := &atlasexec.MigrateApplyParams{
		URL:             act.GetInput("url"),
		DirURL:          act.GetInput("dir"),
		ConfigURL:       act.GetInput("config"),
		Env:             act.GetInput("env"),
		TxMode:          act.GetInput("tx-mode"),  // Hidden param.
		BaselineVersion: act.GetInput("baseline"), // Hidden param.
	}
	// If dir begins with atlas://, this is a cloud-based migration directory.
	// If the user didn't provide a config URL and provided an env name, we'll
	// generate a temporary config file containing a naked env block. This is
	// done so Atlas can report the run to the cloud.
	if strings.HasPrefix(params.DirURL, "atlas://") && params.Env != "" && params.ConfigURL == "" {
		cfg, clean, err := atlasexec.TempFile(`env { name = atlas.env }`, "hcl")
		if err != nil {
			return err
		}
		// nolint:errcheck
		defer clean()
		params.ConfigURL = cfg
	}
	run, err := client.MigrateApply(ctx, params)
	if err != nil {
		act.SetOutput("error", err.Error())
		return err
	}
	if run.Error != "" {
		act.SetOutput("error", run.Error)
		return errors.New(run.Error)
	}
	act.SetOutput("current", run.Current)
	act.SetOutput("target", run.Target)
	act.SetOutput("pending_count", strconv.Itoa(len(run.Pending)))
	act.SetOutput("applied_count", strconv.Itoa(len(run.Applied)))
	act.Infof("Run complete: +%v", run)
	return nil
}

type (
	cloud struct {
		Dir   string
		Tag   string
		Token string
		URL   string
	}
	tmplParams struct {
		Cloud cloud
	}
)
