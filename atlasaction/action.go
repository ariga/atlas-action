// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
	"golang.org/x/oauth2"
)

// Actions holds the runtime for the actions to run.
// This helps to inject the runtime dependencies. Like the SCM client, Atlas client, etc.
type Actions struct {
	Action
	Version string
	Atlas   AtlasExec
}

// Atlas action interface.
type Action interface {
	Logger
	// GetType returns the type of atlasexec trigger Type. e.g. "GITHUB_ACTION"
	// The value is used to identify the type on CI-Run page in Atlas Cloud.
	GetType() atlasexec.TriggerType
	// GetInput returns the value of the input with the given name.
	GetInput(string) string
	// SetOutput sets the value of the output with the given name.
	SetOutput(string, string)
	// TriggerContext returns the context of the trigger event.
	GetTriggerContext() (*TriggerContext, error)
	// AddStepSummary adds a summary to the action step.
	AddStepSummary(string)
}

type Logger interface {
	// Infof logs an info message.
	Infof(string, ...interface{})
	// Warningf logs a warning message.
	Warningf(string, ...interface{})
	// Errorf logs an error message.
	Errorf(string, ...interface{})
	// Fatalf logs a fatal error message and exits the action.
	Fatalf(string, ...interface{})
	// WithFieldsMap returns a new Logger with the given fields.
	WithFieldsMap(map[string]string) Logger
}

// AtlasExec is the interface for the atlas exec client.
type AtlasExec interface {
	// MigrateStatus runs the `migrate status` command.
	MigrateStatus(context.Context, *atlasexec.MigrateStatusParams) (*atlasexec.MigrateStatus, error)
	// MigrateApplySlice runs the `migrate apply` command and returns the successful runs.
	MigrateApplySlice(context.Context, *atlasexec.MigrateApplyParams) ([]*atlasexec.MigrateApply, error)
	// MigrateDown runs the `migrate down` command.
	MigrateDown(context.Context, *atlasexec.MigrateDownParams) (*atlasexec.MigrateDown, error)
	// MigrateLintError runs the `migrate lint` command and fails if there are lint errors.
	MigrateLintError(context.Context, *atlasexec.MigrateLintParams) error
	// MigratePush runs the `migrate push` command.
	MigratePush(context.Context, *atlasexec.MigratePushParams) (string, error)
	// MigrateTest runs the `migrate test` command.
	MigrateTest(context.Context, *atlasexec.MigrateTestParams) (string, error)
	// SchemaPush runs the `schema push` command.
	SchemaPush(context.Context, *atlasexec.SchemaPushParams) (*atlasexec.SchemaPush, error)
	// SchemaTest runs the `schema test` command.
	SchemaTest(context.Context, *atlasexec.SchemaTestParams) (string, error)
	// SchemaPlan runs the `schema plan` command.
	SchemaPlan(context.Context, *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error)
	// SchemaPlanList runs the `schema plan list` command.
	SchemaPlanList(context.Context, *atlasexec.SchemaPlanListParams) ([]atlasexec.SchemaPlanFile, error)
	// SchemaPlanLint runs the `schema plan lint` command.
	SchemaPlanLint(context.Context, *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error)
	// SchemaPlanApprove runs the `schema plan approve` command.
	SchemaPlanApprove(context.Context, *atlasexec.SchemaPlanApproveParams) (*atlasexec.SchemaPlanApprove, error)
	// SchemaApplySlice runs the `schema apply` command.
	SchemaApplySlice(context.Context, *atlasexec.SchemaApplyParams) ([]*atlasexec.SchemaApply, error)
}

// TriggerContext holds the context of the environment the action is running in.
type TriggerContext struct {
	SCM         SCM          // SCM is the source control management system.
	Repo        string       // Repo is the repository name. e.g. "ariga/atlas-action".
	RepoURL     string       // RepoURL is full URL of the repository. e.g. "https://github.com/ariga/atlas-action".
	Branch      string       // Branch name.
	Commit      string       // Commit SHA.
	PullRequest *PullRequest // PullRequest will be available if the event is "pull_request".
	Actor       *Actor       // Actor is the user who triggered the action.
	RerunCmd    string       // RerunCmd is the command to rerun the action.
}

// Actor holds the actor information.
type Actor struct {
	Name string // Username of the actor.
	ID   string // ID of the actor on the SCM.
}

// PullRequest holds the pull request information.
type PullRequest struct {
	Number int    // Pull Request Number
	URL    string // URL of the pull request. e.g "https://github.com/ariga/atlas-action/pull/1"
	Commit string // Latest commit SHA.
}

// SCM holds the source control management system information.
type SCM struct {
	Type   atlasexec.SCMType // Type of the SCM, e.g. "GITHUB" / "GITLAB" / "BITBUCKET".
	APIURL string            // APIURL is the base URL for the SCM API.
}

// New creates a new Actions based on the environment.
func New(getenv func(string) string, w io.Writer) (*Actions, error) {
	a, err := newAction(getenv, w)
	if err != nil {
		return nil, err
	}
	return &Actions{Action: a}, nil
}

// New creates a new Action based on the environment.
func newAction(getenv func(string) string, w io.Writer) (Action, error) {
	if getenv("GITHUB_ACTIONS") == "true" {
		return NewGHAction(getenv, w), nil
	}
	if getenv("CIRCLECI") == "true" {
		return NewCircleCIOrb(getenv, w), nil
	}
	return nil, errors.New("unsupported environment")
}

const (
	// Versioned workflow Commands
	CmdMigratePush  = "migrate/push"
	CmdMigrateLint  = "migrate/lint"
	CmdMigrateApply = "migrate/apply"
	CmdMigrateDown  = "migrate/down"
	CmdMigrateTest  = "migrate/test"
	// Declarative workflow Commands
	CmdSchemaPush        = "schema/push"
	CmdSchemaTest        = "schema/test"
	CmdSchemaPlan        = "schema/plan"
	CmdSchemaPlanApprove = "schema/plan/approve"
	CmdSchemaApply       = "schema/apply"
)

// Run runs the action based on the command name.
func (a *Actions) Run(ctx context.Context, act string) error {
	// Set the working directory if provided.
	if dir := a.WorkingDir(); dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("failed to change working directory: %w", err)
		}
	}
	switch act {
	case CmdMigrateApply:
		return a.MigrateApply(ctx)
	case CmdMigrateDown:
		return a.MigrateDown(ctx)
	case CmdMigratePush:
		return a.MigratePush(ctx)
	case CmdMigrateLint:
		return a.MigrateLint(ctx)
	case CmdMigrateTest:
		return a.MigrateTest(ctx)
	case CmdSchemaPush:
		return a.SchemaPush(ctx)
	case CmdSchemaTest:
		return a.SchemaTest(ctx)
	case CmdSchemaPlan:
		return a.SchemaPlan(ctx)
	case CmdSchemaPlanApprove:
		return a.SchemaPlanApprove(ctx)
	case CmdSchemaApply:
		return a.SchemaApply(ctx)
	default:
		return fmt.Errorf("unknown action: %s", act)
	}
}

// MigrateApply runs the GitHub Action for "ariga/atlas-action/migrate/apply".
func (a *Actions) MigrateApply(ctx context.Context) error {
	params := &atlasexec.MigrateApplyParams{
		ConfigURL:       a.GetInput("config"),
		Env:             a.GetInput("env"),
		Vars:            a.GetVarsInput("vars"),
		Context:         a.DeployRunContext(),
		DirURL:          a.GetInput("dir"),
		URL:             a.GetInput("url"),
		DryRun:          a.GetBoolInput("dry-run"),
		TxMode:          a.GetInput("tx-mode"),  // Hidden param.
		BaselineVersion: a.GetInput("baseline"), // Hidden param.
	}
	runs, err := a.Atlas.MigrateApplySlice(ctx, params)
	if mErr := (&atlasexec.MigrateApplyError{}); errors.As(err, &mErr) {
		// If the error is a MigrateApplyError, we can still get the successful runs.
		runs = mErr.Result
	} else if err != nil {
		a.SetOutput("error", err.Error())
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	for _, run := range runs {
		switch summary, err := RenderTemplate("migrate-apply.tmpl", run); {
		case err != nil:
			a.Errorf("failed to create summary: %v", err)
		default:
			a.AddStepSummary(summary)
		}
		if run.Error != "" {
			a.SetOutput("error", run.Error)
			return errors.New(run.Error)
		}
		a.Infof(`"atlas migrate apply" completed successfully, applied to version %q`, run.Target)
	}
	return nil
}

const (
	StatePending  = "PENDING_USER"
	StateApproved = "APPROVED"
	StateAborted  = "ABORTED"
	StateApplied  = "APPLIED"
)

// MigrateDown runs the GitHub Action for "ariga/atlas-action/migrate/down".
func (a *Actions) MigrateDown(ctx context.Context) (err error) {
	params := &atlasexec.MigrateDownParams{
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
		Context:   a.DeployRunContext(),
		DevURL:    a.GetInput("dev-url"),
		URL:       a.GetInput("url"),
		DirURL:    a.GetInput("dir"),
		ToVersion: a.GetInput("to-version"),
		ToTag:     a.GetInput("to-tag"),
		Amount:    a.GetUin64Input("amount"),
	}
	// Based on the retry configuration values, retry the action if there is an error.
	var (
		interval = a.GetDurationInput("wait-interval")
		timeout  = a.GetDurationInput("wait-timeout")
	)
	if interval == 0 {
		interval = time.Second // Default interval is 1 second.
	}
	var run *atlasexec.MigrateDown
	for started, printed := time.Now(), false; ; {
		run, err = a.Atlas.MigrateDown(ctx, params)
		if err != nil {
			a.SetOutput("error", err.Error())
			return err
		}
		if run.Error != "" {
			a.SetOutput("error", run.Error)
			return errors.New(run.Error)
		}
		// Break the loop if no wait / retry is configured.
		if run.Status != StatePending || timeout == 0 || time.Since(started) >= timeout {
			if timeout != 0 {
				a.Warningf("plan has not been approved in configured waiting period, exiting")
			}
			break
		}
		if !printed {
			printed = true
			a.Infof("plan approval pending, review here: %s", run.URL)
		}
		time.Sleep(interval)
	}
	if run.URL != "" {
		a.SetOutput("url", run.URL)
	}
	switch run.Status {
	case StatePending:
		return fmt.Errorf("plan approval pending, review here: %s", run.URL)
	case StateAborted:
		return fmt.Errorf("plan rejected, review here: %s", run.URL)
	case StateApplied, StateApproved:
		a.Infof(`"atlas migrate down" completed successfully, downgraded to version %q`, run.Target)
		a.SetOutput("current", run.Current)
		a.SetOutput("target", run.Target)
		a.SetOutput("planned_count", strconv.Itoa(len(run.Planned)))
		a.SetOutput("reverted_count", strconv.Itoa(len(run.Reverted)))
	}
	return nil
}

// MigratePush runs the GitHub Action for "ariga/atlas-action/migrate/push"
func (a *Actions) MigratePush(ctx context.Context) error {
	tc, err := a.GetTriggerContext()
	if err != nil {
		return err
	}
	params := &atlasexec.MigratePushParams{
		Name:      a.GetInput("dir-name"),
		DirURL:    a.GetInput("dir"),
		DevURL:    a.GetInput("dev-url"),
		Context:   a.GetRunContext(ctx, tc),
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
	}
	if a.GetBoolInput("latest") {
		// Push the "latest" tag.
		_, err := a.Atlas.MigratePush(ctx, params)
		if err != nil {
			return fmt.Errorf("failed to push directory: %v", err)
		}
	}
	params.Tag = a.GetInput("tag")
	if params.Tag == "" {
		// If the tag is not provided, use the commit SHA.
		params.Tag = params.Context.Commit
	}
	resp, err := a.Atlas.MigratePush(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to push dir tag: %w", err)
	}
	a.Infof(`"atlas migrate push" completed successfully, pushed dir %q to Atlas Cloud`, params.Name)
	a.SetOutput("url", resp)
	return nil
}

// MigrateLint runs the GitHub Action for "ariga/atlas-action/migrate/lint"
func (a *Actions) MigrateLint(ctx context.Context) error {
	dirName := a.GetInput("dir-name")
	if dirName == "" {
		return errors.New("atlasaction: missing required parameter dir-name")
	}
	tc, err := a.GetTriggerContext()
	if err != nil {
		return err
	}
	var (
		resp      bytes.Buffer
		isLintErr bool
	)
	switch err := a.Atlas.MigrateLintError(ctx, &atlasexec.MigrateLintParams{
		DevURL:    a.GetInput("dev-url"),
		DirURL:    a.GetInput("dir"),
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Base:      "atlas://" + dirName,
		Context:   a.GetRunContext(ctx, tc),
		Vars:      a.GetVarsInput("vars"),
		Web:       true,
		Writer:    &resp,
	}); {
	case errors.Is(err, atlasexec.ErrLint):
		isLintErr = true
	case err != nil:
		return err // Non-lint error.
	}
	var payload atlasexec.SummaryReport
	if err := json.NewDecoder(&resp).Decode(&payload); err != nil {
		return fmt.Errorf("decoding payload: %w", err)
	}
	if payload.URL != "" {
		a.SetOutput("report-url", payload.URL)
	}
	if err := a.addChecks(&payload); err != nil {
		return err
	}
	summary, err := RenderTemplate("migrate-lint.tmpl", &payload)
	if err != nil {
		return err
	}
	a.AddStepSummary(summary)
	switch {
	case tc.PullRequest == nil && isLintErr:
		return fmt.Errorf("`atlas migrate lint` completed with errors, see report: %s", payload.URL)
	case tc.PullRequest == nil:
		return nil
	// In case of a pull request, we need to add comments and suggestion to the PR.
	default:
		c := a.GithubClient(tc.Repo, tc.SCM.APIURL)
		if err = c.UpsertComment(ctx, tc.PullRequest, dirName, summary); err != nil {
			a.Errorf("failed to comment on the pull request: %v", err)
		}
		switch files, err := c.ListPullRequestFiles(ctx, tc.PullRequest); {
		case err != nil:
			a.Errorf("failed to list pull request files: %w", err)
		default:
			err = a.addSuggestions(&payload, func(s *Suggestion) error {
				// Add suggestion only if the file is part of the pull request.
				if slices.Contains(files, s.Path) {
					return c.UpsertSuggestion(ctx, tc.PullRequest, s)
				}
				return nil
			})
			if err != nil {
				a.Errorf("failed to add suggestion on the pull request: %v", err)
			}
		}
		if isLintErr {
			return fmt.Errorf("`atlas migrate lint` completed with errors, see report: %s", payload.URL)
		}
		a.Infof("`atlas migrate lint` completed successfully, no issues found")
		return nil
	}
}

// MigrateTest runs the GitHub Action for "ariga/atlas-action/migrate/test"
func (a *Actions) MigrateTest(ctx context.Context) error {
	result, err := a.Atlas.MigrateTest(ctx, &atlasexec.MigrateTestParams{
		DirURL:    a.GetInput("dir"),
		DevURL:    a.GetInput("dev-url"),
		Run:       a.GetInput("run"),
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
	})
	if err != nil {
		return fmt.Errorf("`atlas migrate test` completed with errors:\n%s", err)
	}
	a.Infof("`atlas migrate test` completed successfully, no issues found")
	a.Infof(result)
	return nil
}

// SchemaPush runs the GitHub Action for "ariga/atlas-action/schema/push"
func (a *Actions) SchemaPush(ctx context.Context) error {
	tc, err := a.GetTriggerContext()
	if err != nil {
		return err
	}
	params := &atlasexec.SchemaPushParams{
		Repo:        a.GetAtlasURLInput("schema-name"),
		Description: a.GetInput("description"),
		Version:     a.GetInput("version"),
		DevURL:      a.GetInput("dev-url"),
		Context:     a.GetRunContext(ctx, tc),
		ConfigURL:   a.GetInput("config"),
		Env:         a.GetInput("env"),
		Vars:        a.GetVarsInput("vars"),
		Tag:         a.GetInput("tag"),
	}
	if params.Tag == "" {
		// If the tag is not provided, use the commit SHA.
		params.Tag = params.Context.Commit
	}
	resp, err := a.Atlas.SchemaPush(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to push schema tag: %w", err)
	}
	if a.GetBoolInput("latest") {
		// Push the "latest" tag.
		params.Tag = "latest"
		if _, err := a.Atlas.SchemaPush(ctx, params); err != nil {
			return fmt.Errorf("failed to push schema for latest tag: %v", err)
		}
	}
	a.Infof(`"atlas schema push" completed successfully to: %s`, resp.Link)
	a.SetOutput("link", resp.Link)
	a.SetOutput("slug", resp.Slug)
	a.SetOutput("url", resp.URL)
	return nil
}

// SchemaTest runs the GitHub Action for "ariga/atlas-action/schema/test"
func (a *Actions) SchemaTest(ctx context.Context) error {
	result, err := a.Atlas.SchemaTest(ctx, &atlasexec.SchemaTestParams{
		URL:       a.GetInput("url"),
		DevURL:    a.GetInput("dev-url"),
		Run:       a.GetInput("run"),
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
	})
	if err != nil {
		return fmt.Errorf("`atlas schema test` completed with errors:\n%s", err)
	}
	a.Infof("`atlas schema test` completed successfully, no issues found")
	a.Infof(result)
	return nil
}

// SchemaPlan runs the GitHub Action for "ariga/atlas-action/schema/plan"
func (a *Actions) SchemaPlan(ctx context.Context) error {
	tc, err := a.GetTriggerContext()
	switch {
	case err != nil:
		return fmt.Errorf("unable to get the trigger context: %w", err)
	case tc.PullRequest == nil:
		return fmt.Errorf("the action should be run in a pull request context")
	}
	var plan *atlasexec.SchemaPlan
	params := &atlasexec.SchemaPlanListParams{
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
		Context:   a.GetRunContext(ctx, tc),
		Repo:      a.GetAtlasURLInput("schema-name"),
		DevURL:    a.GetInput("dev-url"),
		From:      a.GetArrayInput("from"),
		To:        a.GetArrayInput("to"),
		Pending:   true,
	}
	switch planFiles, err := a.Atlas.SchemaPlanList(ctx, params); {
	case err != nil:
		return fmt.Errorf("failed to list schema plans: %w", err)
	case len(planFiles) == 1:
		a.Infof("Schema plan already exists, linting the plan %q", planFiles[0].Name)
		plan, err = a.Atlas.SchemaPlanLint(ctx, &atlasexec.SchemaPlanLintParams{
			ConfigURL: params.ConfigURL,
			Env:       params.Env,
			Vars:      params.Vars,
			Context:   params.Context,
			DevURL:    params.DevURL,
			From:      params.From,
			To:        params.To,
			Repo:      params.Repo,
			File:      planFiles[0].URL,
		})
		if err != nil {
			return fmt.Errorf("failed to get the schema plan: %w", err)
		}
	case len(planFiles) == 0:
		name := a.GetInput("name")
	runPlan:
		// Dry run if the name is not provided.
		dryRun := name == ""
		if !dryRun {
			a.Infof("Schema plan does not exist, creating a new one with name %q", name)
		}
		switch plan, err = a.Atlas.SchemaPlan(ctx, &atlasexec.SchemaPlanParams{
			ConfigURL: params.ConfigURL,
			Env:       params.Env,
			Vars:      params.Vars,
			Context:   params.Context,
			DevURL:    params.DevURL,
			From:      params.From,
			To:        params.To,
			Repo:      params.Repo,
			Name:      name,
			DryRun:    dryRun,
			Pending:   !dryRun,
		}); {
		// The schema plan is already in sync.
		case err != nil && strings.Contains(err.Error(), "The current state is synced with the desired state, no changes to be made"):
			// No thing to do.
			a.Infof("The current state is synced with the desired state, no changes to be made")
			return nil
		case err != nil:
			return fmt.Errorf("failed to save schema plan: %w", err)
		case dryRun:
			// Save the plan with the generated name.
			name = fmt.Sprintf("pr-%d-%.8s", tc.PullRequest.Number, plan.File.FromHash)
			goto runPlan
		}
	default:
		for _, f := range planFiles {
			a.Infof("Found schema plan: %s", f.URL)
		}
		return fmt.Errorf("found multiple schema plans, please approve or delete the existing plans")
	}
	// Set the output values from the schema plan.
	a.SetOutput("link", plan.File.Link)
	a.SetOutput("plan", plan.File.URL)
	a.SetOutput("status", plan.File.Status)
	// Report the schema plan to the user and add a comment to the PR.
	summary, err := RenderTemplate("schema-plan.tmpl", map[string]any{
		"Plan":         plan,
		"EnvName":      params.Env,
		"RerunCommand": tc.RerunCmd,
	})
	if err != nil {
		return fmt.Errorf("failed to generate schema plan comment: %w", err)
	}
	a.AddStepSummary(summary)
	// Comment on the PR
	err = a.GithubClient(tc.Repo, tc.SCM.APIURL).
		UpsertComment(ctx, tc.PullRequest, plan.File.Name, summary)
	if err != nil {
		// Don't fail the action if the comment fails.
		// It may be due to the missing permissions.
		a.Errorf("failed to comment on the pull request: %v", err)
	}
	return nil
}

// SchemaPlanApprove runs the GitHub Action for "ariga/atlas-action/schema/plan/approve"
func (a *Actions) SchemaPlanApprove(ctx context.Context) error {
	tc, err := a.GetTriggerContext()
	switch {
	case err != nil:
		return fmt.Errorf("unable to get the trigger context: %w", err)
	case tc.PullRequest != nil:
		return fmt.Errorf("the action should be run in a branch context")
	}
	params := &atlasexec.SchemaPlanApproveParams{
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
		URL:       a.GetInput("plan"),
	}
	if params.URL == "" {
		a.Infof("No plan URL provided, searching for the pending plan")
		switch planFiles, err := a.Atlas.SchemaPlanList(ctx, &atlasexec.SchemaPlanListParams{
			ConfigURL: params.ConfigURL,
			Env:       params.Env,
			Vars:      params.Vars,
			Context:   a.GetRunContext(ctx, tc),
			Repo:      a.GetAtlasURLInput("schema-name"),
			DevURL:    a.GetInput("dev-url"),
			From:      a.GetArrayInput("from"),
			To:        a.GetArrayInput("to"),
			Pending:   true,
		}); {
		case err != nil:
			return fmt.Errorf("failed to list schema plans: %w", err)
		case len(planFiles) == 1:
			params.URL = planFiles[0].URL
		case len(planFiles) == 0:
			a.Infof("No schema plan found")
			return nil
		default:
			for _, f := range planFiles {
				a.Infof("Found schema plan: %s", f.URL)
			}
			return fmt.Errorf("found multiple schema plans, please approve or delete the existing plans")
		}
	}
	result, err := a.Atlas.SchemaPlanApprove(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to approve the schema plan: %w", err)
	}
	// Successfully approved the plan.
	a.Infof("Schema plan approved successfully: %s", result.Link)
	a.SetOutput("link", result.Link)
	a.SetOutput("plan", result.URL)
	a.SetOutput("status", result.Status)
	return nil
}

// SchemaApply runs the GitHub Action for "ariga/atlas-action/schema/apply"
func (a *Actions) SchemaApply(ctx context.Context) error {
	params := &atlasexec.SchemaApplyParams{
		ConfigURL:   a.GetInput("config"),
		Env:         a.GetInput("env"),
		Vars:        a.GetVarsInput("vars"),
		DevURL:      a.GetInput("dev-url"),
		URL:         a.GetInput("url"),
		To:          a.GetInput("to"),
		DryRun:      a.GetBoolInput("dry-run"),
		AutoApprove: a.GetBoolInput("auto-approve"),
		PlanURL:     a.GetInput("plan"),
		TxMode:      a.GetInput("tx-mode"), // Hidden param.
	}
	results, err := a.Atlas.SchemaApplySlice(ctx, params)
	// Any errors will print at the end of execution.
	if mErr := (&atlasexec.SchemaApplyError{}); errors.As(err, &mErr) {
		// If the error is a SchemaApplyError, we can still get the successful runs.
		results = mErr.Result
	}
	for _, result := range results {
		switch summary, err := RenderTemplate("schema-apply.tmpl", result); {
		case err != nil:
			a.Errorf("failed to create summary: %v", err)
		default:
			a.AddStepSummary(summary)
		}
		if result.Error != "" {
			a.SetOutput("error", result.Error)
			return errors.New(result.Error)
		}
		a.Infof(`"atlas schema apply" completed successfully on the target %q`, result.URL)
	}
	// We generate summary for the successful runs.
	// Then fail the action if there is an error.
	if err != nil {
		a.SetOutput("error", err.Error())
		return err
	}
	return nil
}

// WorkingDir returns the working directory for the action.
func (a *Actions) WorkingDir() string {
	return a.GetInput("working-directory")
}

// GetBoolInput returns the boolean input with the given name.
// The input should be a string representation of boolean. (e.g. "true" or "false")
func (a *Actions) GetBoolInput(name string) bool {
	if s := strings.TrimSpace(a.GetInput(name)); s != "" {
		v, err := strconv.ParseBool(s)
		if err == nil {
			return v
		}
		// Exit the action if got invalid input.
		a.Fatalf("the input %q got invalid value for boolean: %v", name, err)
	}
	return false
}

// GetUin64Input returns the uint64 input with the given name.
// The input should be a string representation of uint64. (e.g. "123")
func (a *Actions) GetUin64Input(name string) uint64 {
	if s := strings.TrimSpace(a.GetInput(name)); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err == nil {
			return v
		}
		// Exit the action if got invalid input.
		a.Fatalf("the input %q got invalid value for uint64: %v", name, err)
	}
	return 0
}

// GetDurationInput returns the duration input with the given name.
// The input should be a string representation of time.Duration. (e.g. "1s")
func (a *Actions) GetDurationInput(name string) time.Duration {
	if s := strings.TrimSpace(a.GetInput(name)); s != "" {
		v, err := time.ParseDuration(s)
		if err == nil {
			return v
		}
		// Exit the action if got invalid input.
		a.Fatalf("the input %q got invalid value for duration: %v", name, err)
	}
	return 0
}

// GetAtlasURLInput returns the atlas URL input with the given name.
func (a *Actions) GetAtlasURLInput(name string) string {
	v := a.GetInput(name)
	if v == "" {
		return ""
	}
	return (&url.URL{Scheme: "atlas", Path: v}).String()
}

// GetVarsInput returns the vars input with the given name.
// The input should be a JSON string.
// Example:
// ```yaml
//
//	input: |-
//	  {
//	    "key1": "value1",
//	    "key2": "value2"
//	  }
//
// ```
func (a *Actions) GetVarsInput(name string) atlasexec.VarArgs {
	if s := strings.TrimSpace(a.GetInput(name)); s != "" {
		var v atlasexec.Vars2
		err := json.Unmarshal([]byte(s), &v)
		if err == nil {
			return v
		}
		// Exit the action if got invalid input.
		a.Fatalf("the input %q is not a valid JSON string: %v", name, err)
	}
	return nil
}

// GetArrayInput returns the array input with the given name.
// The input should be a string with new line separated values.
// Example:
// ```yaml
//
//	input: |-
//	  value1
//	  value2
//
// ```
func (a *Actions) GetArrayInput(name string) []string {
	vars := strings.Split(a.GetInput(name), "\n")
	for i, v := range vars {
		vars[i] = strings.TrimSpace(v)
	}
	return slices.DeleteFunc(vars, func(s string) bool {
		return s == ""
	})
}

// GetRunContext returns the run context for the action.
func (a *Actions) GetRunContext(ctx context.Context, tc *TriggerContext) *atlasexec.RunContext {
	url := tc.RepoURL
	if tc.PullRequest != nil {
		url = tc.PullRequest.URL
	}
	rc := &atlasexec.RunContext{
		Repo:    tc.Repo,
		Branch:  tc.Branch,
		Commit:  tc.Commit,
		Path:    a.GetInput("dir"),
		URL:     url,
		SCMType: tc.SCM.Type,
	}
	if a := tc.Actor; a != nil {
		rc.Username, rc.UserID = a.Name, a.ID
	}
	return rc
}

// DeployRunContext returns the run context for the `migrate/apply`, and `migrate/down` actions.
func (a *Actions) DeployRunContext() *atlasexec.DeployRunContext {
	return &atlasexec.DeployRunContext{
		TriggerType:    a.GetType(),
		TriggerVersion: a.Version,
	}
}

// GithubClient returns a new GitHub client for the given repository.
// If the GITHUB_TOKEN is set, it will be used for authentication.
func (a *Actions) GithubClient(repo, baseURL string) *githubAPI {
	httpClient := &http.Client{Timeout: time.Second * 30}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		httpClient.Transport = &oauth2.Transport{
			Base: http.DefaultTransport,
			Source: oauth2.StaticTokenSource(&oauth2.Token{
				AccessToken: token,
			}),
		}
	} else {
		a.Warningf("GITHUB_TOKEN is not set, the action may not have all the permissions")
	}
	if baseURL == "" {
		baseURL = defaultGHApiUrl
	}
	return &githubAPI{
		baseURL: baseURL,
		repo:    repo,
		client:  httpClient,
	}
}

// RequiredInputs returns an error if any of the given inputs are missing.
func (a *Actions) RequiredInputs(input ...string) error {
	for _, in := range input {
		if strings.TrimSpace(a.GetInput(in)) == "" {
			return fmt.Errorf("required input %q is missing", in)
		}
	}
	return nil
}

// addChecks runs annotations to the trigger event pull request for the given payload.
func (a *Actions) addChecks(lint *atlasexec.SummaryReport) error {
	// Get the directory path from the lint report.
	dir := path.Join(a.WorkingDir(), lint.Env.Dir)
	for _, file := range lint.Files {
		filePath := path.Join(dir, file.Name)
		if file.Error != "" && len(file.Reports) == 0 {
			a.WithFieldsMap(map[string]string{
				"file": filePath,
				"line": "1",
			}).Errorf(file.Error)
			continue
		}
		for _, report := range file.Reports {
			for _, diag := range report.Diagnostics {
				msg := diag.Text
				if diag.Code != "" {
					msg = fmt.Sprintf("%v (%v)\n\nDetails: https://atlasgo.io/lint/analyzers#%v", msg, diag.Code, diag.Code)
				}
				lines := strings.Split(file.Text[:diag.Pos], "\n")
				logger := a.WithFieldsMap(map[string]string{
					"file":  filePath,
					"line":  strconv.Itoa(max(1, len(lines))),
					"title": report.Text,
				})
				if file.Error != "" {
					logger.Errorf(msg)
				} else {
					logger.Warningf(msg)
				}
			}
		}
	}
	return nil
}

type Suggestion struct {
	ID        string // Unique identifier for the suggestion.
	Path      string // File path.
	StartLine int    // Start line numbers for the suggestion.
	Line      int    // End line number for the suggestion.
	Comment   string // Comment body.
}

// addSuggestions returns the suggestions from the lint report.
func (a *Actions) addSuggestions(lint *atlasexec.SummaryReport, fn func(*Suggestion) error) (err error) {
	if !slices.ContainsFunc(lint.Files, func(f *atlasexec.FileReport) bool {
		return len(f.Reports) > 0
	}) {
		// No reports to add suggestions.
		return nil
	}
	dir := a.WorkingDir()
	for _, file := range lint.Files {
		filePath := path.Join(dir, lint.Env.Dir, file.Name)
		for reportIdx, report := range file.Reports {
			for _, f := range report.SuggestedFixes {
				if f.TextEdit == nil {
					continue
				}
				s := Suggestion{Path: filePath, ID: f.Message}
				if f.TextEdit.End <= f.TextEdit.Line {
					s.Line = f.TextEdit.Line
				} else {
					s.StartLine = f.TextEdit.Line
					s.Line = f.TextEdit.End
				}
				s.Comment, err = RenderTemplate("suggestion.tmpl", map[string]any{
					"Fix": f,
					"Dir": lint.Env.Dir,
				})
				if err != nil {
					return fmt.Errorf("failed to render suggestion: %w", err)
				}
				if err = fn(&s); err != nil {
					return fmt.Errorf("failed to process suggestion: %w", err)
				}
			}
			for diagIdx, d := range report.Diagnostics {
				for _, f := range d.SuggestedFixes {
					if f.TextEdit == nil {
						continue
					}
					s := Suggestion{Path: filePath, ID: f.Message}
					if f.TextEdit.End <= f.TextEdit.Line {
						s.Line = f.TextEdit.Line
					} else {
						s.StartLine = f.TextEdit.Line
						s.Line = f.TextEdit.End
					}
					s.Comment, err = RenderTemplate("suggestion.tmpl", map[string]any{
						"Fix":    f,
						"Dir":    lint.Env.Dir,
						"File":   file,
						"Report": reportIdx,
						"Diag":   diagIdx,
					})
					if err != nil {
						return fmt.Errorf("failed to render suggestion: %w", err)
					}
					if err = fn(&s); err != nil {
						return fmt.Errorf("failed to process suggestion: %w", err)
					}
				}
			}
		}
	}
	return nil
}

func execTime(start, end time.Time) string {
	return end.Sub(start).String()
}

func appliedStmts(a *atlasexec.MigrateApply) int {
	total := 0
	for _, file := range a.Applied {
		total += len(file.Applied)
	}
	return total
}

var (
	//go:embed comments/*.tmpl
	comments     embed.FS
	commentsTmpl = template.Must(
		template.New("comments").
			Funcs(template.FuncMap{
				"execTime":     execTime,
				"appliedStmts": appliedStmts,
				"filterIssues": func(steps []*atlasexec.StepReport) []*atlasexec.StepReport {
					result := make([]*atlasexec.StepReport, 0, len(steps))
					for _, s := range steps {
						switch {
						case s.Error != "":
							result = append(result, s)
						case s.Result == nil: // No result.
						case s.Result.Error != "" || len(s.Result.Reports) > 0:
							result = append(result, s)
						}
					}
					return result
				},
				"stepIsError": func(s *atlasexec.StepReport) bool {
					return s.Error != "" || (s.Result != nil && s.Result.Error != "")
				},
				"firstUpper": func(s string) string {
					if s == "" {
						return ""
					}
					return strings.ToUpper(s[:1]) + s[1:]
				},
				"assetsImage": func(s string) (string, error) {
					u, err := url.Parse("https://release.ariga.io/images/assets")
					if err != nil {
						return "", err
					}
					u = u.JoinPath(s)
					u.RawQuery = "v=1" // Cache buster.
					return u.String(), nil
				},
				"join": strings.Join,
				"codeblock": func(lang, code string) string {
					return fmt.Sprintf("<pre lang=%q><code>%s</code></pre>", lang, code)
				},
				"details": func(label, details string) string {
					return fmt.Sprintf("<details><summary>%s</summary>%s</details>", label, details)
				},
				"link": func(text, href string) string {
					return fmt.Sprintf(`<a href=%q target="_blank">%s</a>`, href, text)
				},
				"image": func(args ...any) (string, error) {
					var attrs string
					var src any
					switch len(args) {
					case 1:
						src, attrs = args[0], fmt.Sprintf("src=%q", args...)
					case 2:
						src, attrs = args[1], fmt.Sprintf("width=%[1]q height=%[1]q src=%[2]q", args...)
					case 3:
						src, attrs = args[2], fmt.Sprintf("width=%q height=%q src=%q", args...)
					case 4:
						src, attrs = args[3], fmt.Sprintf("width=%q height=%q alt=%q src=%q", args...)
					default:
						return "", fmt.Errorf("invalid number of arguments %d", len(args))
					}
					// Wrap the image in a picture element to avoid
					// clicking on the image to view the full size.
					return fmt.Sprintf(`<picture><source media="(prefers-color-scheme: light)" srcset=%q><img %s/></picture>`, src, attrs), nil
				},
			}).
			ParseFS(comments, "comments/*.tmpl"),
	)
)

// RenderTemplate renders the given template with the data.
func RenderTemplate(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := commentsTmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type (
	githubIssueComment struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}

	pullRequestComment struct {
		ID        int    `json:"id,omitempty"`
		Body      string `json:"body"`
		Path      string `json:"path"`
		CommitID  string `json:"commit_id,omitempty"`
		StartLine int    `json:"start_line,omitempty"`
		Line      int    `json:"line,omitempty"`
	}

	pullRequestFile struct {
		Name string `json:"filename"`
	}

	githubAPI struct {
		baseURL string
		repo    string
		client  *http.Client
	}
)

const defaultGHApiUrl = "https://api.github.com"

func (g *githubAPI) UpsertComment(ctx context.Context, pr *PullRequest, id, comment string) error {
	comments, err := g.getIssueComments(ctx, pr)
	if err != nil {
		return err
	}
	var (
		marker = commentMarker(id)
		body   = strings.NewReader(fmt.Sprintf(`{"body": %q}`, comment+"\n"+marker))
	)
	if found := slices.IndexFunc(comments, func(c githubIssueComment) bool {
		return strings.Contains(c.Body, marker)
	}); found != -1 {
		return g.updateIssueComment(ctx, comments[found].ID, body)
	}
	return g.createIssueComment(ctx, pr, body)
}

func (g *githubAPI) getIssueComments(ctx context.Context, pr *PullRequest) ([]githubIssueComment, error) {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error querying github comments with %v/%v, %w", g.repo, pr.Number, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading PR issue comments from %v/%v, %v", g.repo, pr.Number, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v when calling GitHub API", res.StatusCode)
	}
	var comments []githubIssueComment
	if err = json.Unmarshal(buf, &comments); err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v from %v, %w", g.repo, pr.Number, string(buf), err)
	}
	return comments, nil
}

func (g *githubAPI) createIssueComment(ctx context.Context, pr *PullRequest, content io.Reader) error {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, content)
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return err
}

// updateIssueComment updates issue comment with the given id.
func (g *githubAPI) updateIssueComment(ctx context.Context, id int, content io.Reader) error {
	url := fmt.Sprintf("%v/repos/%v/issues/comments/%v", g.baseURL, g.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, content)
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return err
}

// UpsertSuggestion creates or updates a suggestion review comment on trigger event pull request.
func (g *githubAPI) UpsertSuggestion(ctx context.Context, pr *PullRequest, s *Suggestion) error {
	marker := commentMarker(s.ID)
	body := fmt.Sprintf("%s\n%s", s.Comment, marker)
	// TODO: Listing the comments only once and updating the comment in the same call.
	comments, err := g.listReviewComments(ctx, pr)
	if err != nil {
		return err
	}
	// Search for the comment marker in the comments list.
	// If found, update the comment with the new suggestion.
	// If not found, create a new suggestion comment.
	found := slices.IndexFunc(comments, func(c pullRequestComment) bool {
		return c.Path == s.Path && strings.Contains(c.Body, marker)
	})
	if found != -1 {
		if err := g.updateReviewComment(ctx, comments[found].ID, body); err != nil {
			return err
		}
		return nil
	}
	buf, err := json.Marshal(pullRequestComment{
		Body:      body,
		Path:      s.Path,
		CommitID:  pr.Commit,
		Line:      s.Line,
		StartLine: s.StartLine,
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return err
}

// listReviewComments for the trigger event pull request.
func (g *githubAPI) listReviewComments(ctx context.Context, pr *PullRequest) ([]pullRequestComment, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	var comments []pullRequestComment
	if err = json.NewDecoder(res.Body).Decode(&comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// updateReviewComment updates the review comment with the given id.
func (g *githubAPI) updateReviewComment(ctx context.Context, id int, body string) error {
	type pullRequestUpdate struct {
		Body string `json:"body"`
	}
	b, err := json.Marshal(pullRequestUpdate{Body: body})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/pulls/comments/%v", g.baseURL, g.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return err
}

// ListPullRequestFiles return paths of the files in the trigger event pull request.
func (g *githubAPI) ListPullRequestFiles(ctx context.Context, pr *PullRequest) ([]string, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/files", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	var files []pullRequestFile
	if err = json.NewDecoder(res.Body).Decode(&files); err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i := range files {
		paths[i] = files[i].Name
	}
	return paths, nil
}

// OpeningPullRequest returns the latest open pull request for the given branch.
func (g *githubAPI) OpeningPullRequest(ctx context.Context, branch string) (*PullRequest, error) {
	owner, _, err := g.ownerRepo()
	if err != nil {
		return nil, err
	}
	// Get open pull requests for the branch.
	url := fmt.Sprintf("%s/repos/%s/pulls?state=open&head=%s:%s&sort=created&direction=desc&per_page=1&page=1",
		g.baseURL, g.repo, owner, branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error calling GitHub API: %w", err)
	}
	defer res.Body.Close()
	switch buf, err := io.ReadAll(res.Body); {
	case err != nil:
		return nil, fmt.Errorf("error reading open pull requests: %w", err)
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("unexpected status code: %d when calling GitHub API", res.StatusCode)
	default:
		var resp []struct {
			Url    string `json:"url"`
			Number int    `json:"number"`
			Head   struct {
				Sha string `json:"sha"`
			} `json:"head"`
		}
		if err = json.Unmarshal(buf, &resp); err != nil {
			return nil, err
		}
		if len(resp) == 0 {
			return nil, nil
		}
		return &PullRequest{
			Number: resp[0].Number,
			URL:    resp[0].Url,
			Commit: resp[0].Head.Sha,
		}, nil
	}
}

func (g *githubAPI) ownerRepo() (string, string, error) {
	s := strings.Split(g.repo, "/")
	if len(s) != 2 {
		return "", "", fmt.Errorf("GITHUB_REPOSITORY must be in the format of 'owner/repo'")
	}
	return s[0], s[1], nil
}

// commentMarker creates a hidden marker to identify the comment as one created by this action.
func commentMarker(id string) string {
	return fmt.Sprintf(`<!-- generated by ariga/atlas-action for %v -->`, id)
}
