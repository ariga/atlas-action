// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"strconv"
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
	// Cloud-based migration directory.
	if act.GetInput("dir-name") != "" {
		if params.DirURL != "" {
			return errors.New("dir and dir-name are mutually exclusive")
		}
		// Cloud-based migrations are currently based on creating a temporary atlas.hcl
		// file therefore it cannot be used with a user-supplied config.
		if params.ConfigURL != "" {
			return errors.New("config and dir-name are mutually exclusive")
		}
		var buf bytes.Buffer
		if err := config.Execute(&buf, &tmplParams{
			Cloud: cloud{
				Dir:   act.GetInput("dir-name"),
				Tag:   act.GetInput("tag"),
				Token: act.GetInput("cloud-token"), // Hidden param.
				URL:   act.GetInput("cloud-url"),   // Hidden param. Used for testing.
			},
		}); err != nil {
			return err
		}
		cfg, clean, err := atlasexec.TempFile(buf.String(), "hcl")
		if err != nil {
			return err
		}
		// nolint:errcheck
		defer clean()
		params.ConfigURL = cfg
		if params.Env == "" {
			params.Env = "atlas"
		}
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
