// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"ariga.io/atlas-action/atlasaction/cloud"
	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/fatih/color"
)

type (
	// Actions holds the runtime for the actions to run.
	// This helps to inject the runtime dependencies. Like the SCM client, Atlas client, etc.
	Actions struct {
		Action
		Version     string
		Atlas       AtlasExec
		CloudClient func(string, string, *atlasexec.Version) CloudClient
	}

	// Action interface for Atlas.
	Action interface {
		Logger
		// GetType returns the type of atlasexec trigger Type. e.g. "GITHUB_ACTION"
		// The value is used to identify the type on CI-Run page in Atlas Cloud.
		GetType() atlasexec.TriggerType
		// Getenv returns the value of the environment variable with the given name.
		Getenv(string) string
		// GetInput returns the value of the input with the given name.
		GetInput(string) string
		// SetOutput sets the value of the output with the given name.
		SetOutput(string, string)
		// GetTriggerContext returns the context of the trigger event.
		GetTriggerContext(context.Context) (*TriggerContext, error)
	}

	// Reporter is an interface for reporting the status of the actions.
	Reporter interface {
		MigrateApply(context.Context, *atlasexec.MigrateApply)
		MigrateLint(context.Context, *atlasexec.SummaryReport)
		SchemaPlan(context.Context, *atlasexec.SchemaPlan)
		SchemaApply(context.Context, *atlasexec.SchemaApply)
	}
	// SCMClient contains methods for interacting with SCM platforms (GitHub, Gitlab etc...).
	SCMClient interface {
		// CommentLint comments on the pull request with the lint report.
		CommentLint(context.Context, *TriggerContext, *atlasexec.SummaryReport) error
		// CommentPlan comments on the pull request with the schema plan.
		CommentPlan(context.Context, *TriggerContext, *atlasexec.SchemaPlan) error
	}
	Logger interface {
		// Infof logs an info message.
		Infof(string, ...interface{})
		// Warningf logs a warning message.
		Warningf(string, ...interface{})
		// Errorf logs an error message.
		Errorf(string, ...interface{})
		// Fatalf logs a fatal error message and exits the action.
		Fatalf(string, ...interface{})
	}

	// AtlasExec is the interface for the atlas exec client.
	AtlasExec interface {
		// Version returns the version of the atlas binary.
		Version(ctx context.Context) (*atlasexec.Version, error)
		// Login runs the `login` command.
		Login(ctx context.Context, params *atlasexec.LoginParams) error
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
		// SchemaInspect runs the `schema inspect` command.
		SchemaInspect(ctx context.Context, params *atlasexec.SchemaInspectParams) (string, error)
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

	// CloudClient lets an action talk to Atlas Cloud.
	CloudClient interface {
		// SnapshotHash returns the latest snapshot hash for a monitored schema.
		SnapshotHash(context.Context, *cloud.SnapshotHashInput) (string, error)
		// PushSnapshot pushes a new snapshot version of a monitored schema to the cloud.
		PushSnapshot(context.Context, *cloud.PushSnapshotInput) (string, error)
	}

	// TriggerContext holds the context of the environment the action is running in.
	TriggerContext struct {
		Act         Action       // Act is the action that is running.
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
	Actor struct {
		Name string // Username of the actor.
		ID   string // ID of the actor on the SCM.
	}

	// PullRequest holds the pull request information.
	PullRequest struct {
		Number int    // Pull Request Number
		URL    string // URL of the pull request. e.g "https://github.com/ariga/atlas-action/pull/1"
		Commit string // Latest commit SHA.
		Body   string // Body (description) of the pull request.
	}
)

// AtlasDirectives returns any directives that are
// present in the pull request body. For example:
//
//	/atlas:nolint destructive
func (p *PullRequest) AtlasDirectives() (ds []string) {
	const prefix = "/atlas:"
	for _, l := range strings.Split(p.Body, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, prefix) && !strings.HasSuffix(l, prefix) {
			ds = append(ds, l[1:])
		}
	}
	return ds
}

// SCM holds the source control management system information.
type SCM struct {
	Type   atlasexec.SCMType // Type of the SCM, e.g. "GITHUB" / "GITLAB" / "BITBUCKET".
	APIURL string            // APIURL is the base URL for the SCM API.
}

// New creates a new Actions based on the environment.
func New(opts ...Option) (*Actions, error) {
	cfg := &config{getenv: os.Getenv, out: os.Stdout}
	opts = append(opts, WithRuntimeAction())
	for _, o := range opts {
		o(cfg)
		if cfg.err != nil {
			return nil, cfg.err
		}
	}
	if cfg.action == nil {
		return nil, errors.New("atlasaction: no action found for the current environment")
	}
	return &Actions{
		Action:      cfg.action,
		Atlas:       cfg.atlas,
		CloudClient: cfg.cloudClient,
		Version:     cfg.version,
	}, nil
}

// WithGetenv specifies how to obtain environment variables.
func WithGetenv(getenv func(string) string) Option {
	return func(c *config) { c.getenv = getenv }
}

// WithOut specifies where to print to.
func WithOut(out io.Writer) Option {
	return func(c *config) { c.out = out }
}

// WithAction sets the Action to use.
func WithAction(a Action) Option {
	return func(c *config) { c.action = a }
}

// WithRuntimeAction detects the action based on the environment.
func WithRuntimeAction() Option {
	return func(c *config) {
		switch {
		case c.action != nil:
			// Do nothing. Action is already set.
		case c.getenv("GITHUB_ACTIONS") == "true":
			c.action = NewGHAction(c.getenv, c.out)
		case c.getenv("CIRCLECI") == "true":
			c.action = NewCircleCIOrb(c.getenv, c.out)
		case c.getenv("GITLAB_CI") == "true":
			c.action = NewGitlabCI(c.getenv, c.out)
		case c.getenv("BITBUCKET_PIPELINE_UUID") != "":
			c.action = NewBitBucketPipe(c.getenv, c.out)
		}
	}
}

// WithAtlasPath sets the path to the atlas binary.
func WithAtlasPath(bin string) Option {
	return func(c *config) {
		c.atlas, c.err = atlasexec.NewClient("", bin)
	}
}

// WithAtlas sets the AtlasExec to use.
func WithAtlas(a AtlasExec) Option {
	return func(c *config) { c.atlas = a }
}

// WithCloudClient specifies how to obtain a CloudClient given the name of the token input variable.
func WithCloudClient[T CloudClient](cc func(token, version, cliVersion string) T) Option {
	return func(c *config) {
		c.cloudClient = func(token, version string, v *atlasexec.Version) CloudClient {
			return cc(token, version, v.String())
		}
	}
}

// WithVersion specifies the version of the Actions.
func WithVersion(v string) Option {
	return func(c *config) { c.version = v }
}

// ErrNoSCM is returned when no SCM client is found.
var ErrNoSCM = errors.New("atlasaction: no SCM client found")

type (
	config struct {
		getenv      func(string) string
		out         io.Writer
		action      Action
		atlas       AtlasExec
		cloudClient func(string, string, *atlasexec.Version) CloudClient
		version     string
		err         error // the error occurred during the configuration.
	}
	Option func(*config)
)

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
	// Montioring Commands
	CmdMonitorSchema = "monitor/schema"
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
	case CmdMonitorSchema:
		return a.MonitorSchema(ctx)
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
		RevisionsSchema: a.GetInput("revisions-schema"),
		AllowDirty:      a.GetBoolInput("allow-dirty"),
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
		if r, ok := a.Action.(Reporter); ok {
			r.MigrateApply(ctx, run)
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
		ConfigURL:       a.GetInput("config"),
		Env:             a.GetInput("env"),
		Vars:            a.GetVarsInput("vars"),
		Context:         a.DeployRunContext(),
		DevURL:          a.GetInput("dev-url"),
		URL:             a.GetInput("url"),
		DirURL:          a.GetInput("dir"),
		ToVersion:       a.GetInput("to-version"),
		ToTag:           a.GetInput("to-tag"),
		Amount:          a.GetUin64Input("amount"),
		RevisionsSchema: a.GetInput("revisions-schema"),
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
	tc, err := a.GetTriggerContext(ctx)
	if err != nil {
		return err
	}
	rc := tc.GetRunContext()
	rc.Path = a.GetInput("dir")
	params := &atlasexec.MigratePushParams{
		Context:   rc,
		Name:      a.GetInput("dir-name"),
		DirURL:    a.GetInput("dir"),
		DevURL:    a.GetInput("dev-url"),
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
	tc, err := a.GetTriggerContext(ctx)
	if err != nil {
		return err
	}
	var (
		resp      bytes.Buffer
		isLintErr bool
	)
	rc := tc.GetRunContext()

	rc.Path = a.GetInput("dir")
	switch err := a.Atlas.MigrateLintError(ctx, &atlasexec.MigrateLintParams{
		Context:   rc,
		DevURL:    a.GetInput("dev-url"),
		DirURL:    a.GetInput("dir"),
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Base:      a.GetAtlasURLInput("dir-name", "tag"),
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
	if r, ok := a.Action.(Reporter); ok {
		r.MigrateLint(ctx, &payload)
	}
	if tc.PullRequest != nil {
		// In case of a pull request, we need to add comments and suggestion to the PR.
		switch c, err := tc.SCMClient(); {
		case errors.Is(err, ErrNoSCM):
		case err != nil:
			return err
		default:
			if err = c.CommentLint(ctx, tc, &payload); err != nil {
				a.Errorf("failed to comment on the pull request: %v", err)
			}
		}
	}
	if isLintErr {
		return fmt.Errorf("`atlas migrate lint` completed with errors, see report: %s", payload.URL)
	}
	a.Infof("`atlas migrate lint` completed successfully, no issues found")
	return nil
}

// MigrateTest runs the GitHub Action for "ariga/atlas-action/migrate/test"
func (a *Actions) MigrateTest(ctx context.Context) error {
	result, err := a.Atlas.MigrateTest(ctx, &atlasexec.MigrateTestParams{
		DirURL:          a.GetInput("dir"),
		DevURL:          a.GetInput("dev-url"),
		Run:             a.GetInput("run"),
		ConfigURL:       a.GetInput("config"),
		Env:             a.GetInput("env"),
		Vars:            a.GetVarsInput("vars"),
		RevisionsSchema: a.GetInput("revisions-schema"),
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
	tc, err := a.GetTriggerContext(ctx)
	if err != nil {
		return err
	}
	params := &atlasexec.SchemaPushParams{
		Context:     tc.GetRunContext(),
		Name:        a.GetInput("schema-name"),
		Description: a.GetInput("description"),
		Version:     a.GetInput("version"),
		URL:         a.GetArrayInput("url"),
		Schema:      a.GetArrayInput("schema"),
		DevURL:      a.GetInput("dev-url"),
		ConfigURL:   a.GetInput("config"),
		Env:         a.GetInput("env"),
		Vars:        a.GetVarsInput("vars"),
	}
	if a.GetBoolInput("latest") {
		// Push the "latest" tag.
		params.Tag = "latest"
		if _, err := a.Atlas.SchemaPush(ctx, params); err != nil {
			return fmt.Errorf("failed to push schema for latest tag: %v", err)
		}
	}
	params.Tag = a.GetInput("tag")
	if params.Tag == "" {
		// If the tag is not provided, use the commit SHA.
		params.Tag = params.Context.Commit
	}
	resp, err := a.Atlas.SchemaPush(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to push schema tag: %w", err)
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
	tc, err := a.GetTriggerContext(ctx)
	switch {
	case err != nil:
		return fmt.Errorf("unable to get the trigger context: %w", err)
	case tc.PullRequest == nil:
		return fmt.Errorf("the action should be run in a pull request context")
	}
	var plan *atlasexec.SchemaPlan
	params := &atlasexec.SchemaPlanListParams{
		Context:   tc.GetRunContext(),
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
		Repo:      a.GetAtlasURLInput("schema-name"),
		DevURL:    a.GetInput("dev-url"),
		Schema:    a.GetArrayInput("schema"),
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
			Schema:    params.Schema,
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
			ConfigURL:  params.ConfigURL,
			Env:        params.Env,
			Vars:       params.Vars,
			Context:    params.Context,
			DevURL:     params.DevURL,
			Schema:     params.Schema,
			From:       params.From,
			To:         params.To,
			Repo:       params.Repo,
			Name:       name,
			DryRun:     dryRun,
			Pending:    !dryRun,
			Directives: tc.PullRequest.AtlasDirectives(),
		}); {
		// The schema plan is already in sync.
		case err != nil && strings.Contains(err.Error(), "The current state is synced with the desired state, no changes to be made"):
			// Nothing to do.
			a.Infof("The current state is synced with the desired state, no changes to be made")
			return nil
		case err != nil:
			return fmt.Errorf("failed to save schema plan: %w", err)
		case dryRun:
			// Save the plan with the generated name.
			name = fmt.Sprintf("pr-%d-%.8s", tc.PullRequest.Number,
				// RFC4648 base64url encoding without padding.
				strings.NewReplacer("+", "-", "/", "_", "=", "").Replace(plan.File.FromHash))
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
	if r, ok := a.Action.(Reporter); ok {
		r.SchemaPlan(ctx, plan)
	}
	switch c, err := tc.SCMClient(); {
	case errors.Is(err, ErrNoSCM):
	case err != nil:
		return err
	default:
		err = c.CommentPlan(ctx, tc, plan)
		if err != nil {
			// Don't fail the action if the comment fails.
			// It may be due to the missing permissions.
			a.Errorf("failed to comment on the pull request: %v", err)
		}
	}
	if plan.Lint != nil {
		if errs := plan.Lint.Errors(); len(errs) > 0 {
			return fmt.Errorf("`atlas schema plan` completed with lint errors:\n%v", errors.Join(errs...))
		}
	}
	return nil
}

// SchemaPlanApprove runs the GitHub Action for "ariga/atlas-action/schema/plan/approve"
func (a *Actions) SchemaPlanApprove(ctx context.Context) error {
	tc, err := a.GetTriggerContext(ctx)
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
			Context:   tc.GetRunContext(),
			ConfigURL: params.ConfigURL,
			Env:       params.Env,
			Vars:      params.Vars,
			Repo:      a.GetAtlasURLInput("schema-name"),
			DevURL:    a.GetInput("dev-url"),
			Schema:    a.GetArrayInput("schema"),
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
		Schema:      a.GetArrayInput("schema"),
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
		if r, ok := a.Action.(Reporter); ok {
			r.SchemaApply(ctx, result)
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

// MonitorSchema runs the Action for "ariga/atlas-action/monitor/schema"
func (a *Actions) MonitorSchema(ctx context.Context) error {
	if err := a.RequiredInputs("cloud-token"); err != nil {
		return err
	}
	if err := a.Atlas.Login(ctx, &atlasexec.LoginParams{
		Token: a.GetInput("cloud-token"),
	}); err != nil {
		return fmt.Errorf("failed to login to Atlas Cloud: %w", err)
	}
	params := &atlasexec.SchemaInspectParams{
		URL:       a.GetInput("url"),
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Schema:    a.GetArrayInput("schemas"),
		Exclude:   a.GetArrayInput("exclude"),
		Format:    `{{ printf "# %s\n# %s\n%s" .RedactedURL .Hash .MarshalHCL }}`,
	}
	if (params.ConfigURL != "" || params.Env != "") && params.URL != "" {
		return errors.New("only one of the inputs 'config' or 'url' must be given")
	}
	hcl, err := a.Atlas.SchemaInspect(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to inspect the schema: %w", err)
	}
	var redactedURL, hash string
	if parts := strings.SplitN(hcl, "\n", 3); len(parts) != 3 {
		return fmt.Errorf("invalid inspect output, expect 3 lines, got %d", len(parts))
	} else {
		redactedURL = strings.TrimPrefix(parts[0], "# ")
		hash = strings.TrimPrefix(parts[1], "# ")
		hcl = parts[2]
	}
	cc, err := a.cloudClient(ctx, "cloud-token")
	if err != nil {
		return err
	}
	id := cloud.ScopeIdent{
		URL:     redactedURL,
		Schemas: params.Schema,
		Exclude: params.Exclude,
		ExtID:   a.GetInput("slug"),
	}
	h, err := cc.SnapshotHash(ctx, &cloud.SnapshotHashInput{ScopeIdent: id})
	if err != nil {
		return fmt.Errorf("failed to get the schema snapshot hash: %w", err)
	}
	input := &cloud.PushSnapshotInput{
		ScopeIdent: id,
		HashMatch:  strings.HasPrefix(h, "h1:") && OldAgentHash(hcl) == h || hash == h,
	}
	if !input.HashMatch {
		input.Snapshot = &cloud.SnapshotInput{Hash: hash, HCL: hcl}
	}
	u, err := cc.PushSnapshot(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to push the schema snapshot: %w", err)
	}
	a.SetOutput("url", u)
	a.Infof(`"atlas monitor schema" completed successfully, see the schema in Atlas Cloud: %s`, u)
	return nil
}

func (a *Actions) cloudClient(ctx context.Context, tokenInput string) (CloudClient, error) {
	t := a.GetInput(tokenInput)
	if t == "" {
		return nil, fmt.Errorf("missing required input: %q", tokenInput)
	}
	v, err := a.Atlas.Version(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get the atlas version: %w", err)
	}
	return a.CloudClient(t, a.Version, v), nil
}

// OldAgentHash computes a hash of the input. Used by the agent to determine if a new snapshot is needed.
//
// Only here for backwards compatability as for new snapshots the Atlas CLI computed hash is used.
func OldAgentHash(src string) string {
	sha := sha256.New()
	sha.Write([]byte(src))
	return "h1:" + base64.StdEncoding.EncodeToString(sha.Sum(nil))
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
// paramsName is the list of input names to be added as query parameters.
func (a *Actions) GetAtlasURLInput(name string, paramsName ...string) string {
	v := a.GetInput(name)
	if v == "" {
		return ""
	}
	u := (&url.URL{Scheme: "atlas", Path: v})
	if len(paramsName) > 0 {
		q := u.Query()
		for _, p := range paramsName {
			if v := a.GetInput(p); v != "" {
				q.Set(p, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// GetURLInput tries to parse the input as URL. In case of a parsing error,
// this function ensures the error does not leak any sensitive information.
func (a *Actions) GetURLInput(name string) (*url.URL, error) {
	u, err := url.Parse(a.GetInput(name))
	if err != nil {
		// Ensure to not leak any sensitive information into logs.
		if uerr := new(url.Error); errors.As(err, &uerr) {
			err = uerr.Err
		}
		a.Fatalf("the input %q is not a valid URL string: %v", name, err)
	}
	return u, nil
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

// DeployRunContext returns the run context for the `migrate/apply`, and `migrate/down` actions.
func (a *Actions) DeployRunContext() *atlasexec.DeployRunContext {
	return &atlasexec.DeployRunContext{
		TriggerType:    a.GetType(),
		TriggerVersion: a.Version,
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

// SCMClient returns a Client to interact with the SCM.
func (tc *TriggerContext) SCMClient() (SCMClient, error) {
	switch tc.SCM.Type {
	case atlasexec.SCMTypeGithub:
		token := tc.Act.Getenv("GITHUB_TOKEN")
		if token == "" {
			tc.Act.Warningf("GITHUB_TOKEN is not set, the action may not have all the permissions")
		}
		return GitHubClient(tc.Repo, tc.SCM.APIURL, token)
	case atlasexec.SCMTypeGitlab:
		token := tc.Act.Getenv("GITLAB_TOKEN")
		if token == "" {
			tc.Act.Warningf("GITLAB_TOKEN is not set, the action may not have all the permissions")
		}
		return GitLabClient(
			tc.Act.Getenv("CI_PROJECT_ID"),
			tc.SCM.APIURL,
			token,
		)
	case atlasexec.SCMTypeBitbucket:
		token := tc.Act.Getenv("BITBUCKET_ACCESS_TOKEN")
		if token == "" {
			tc.Act.Warningf("BITBUCKET_ACCESS_TOKEN is not set, the action may not have all the permissions")
		}
		return BitbucketClient(
			tc.Act.Getenv("BITBUCKET_WORKSPACE"),
			tc.Act.Getenv("BITBUCKET_REPO_SLUG"),
			token,
		)
	default:
		return nil, ErrNoSCM // Not implemented yet.
	}
}

// GetRunContext returns the run context for the action.
func (tc *TriggerContext) GetRunContext() *atlasexec.RunContext {
	rc := &atlasexec.RunContext{
		URL:     tc.RepoURL,
		Repo:    tc.Repo,
		Branch:  tc.Branch,
		Commit:  tc.Commit,
		SCMType: tc.SCM.Type,
	}
	if pr := tc.PullRequest; pr != nil {
		rc.URL = pr.URL
	}
	if a := tc.Actor; a != nil {
		rc.Username, rc.UserID = a.Name, a.ID
	}
	return rc
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

func filterIssues(steps []*atlasexec.StepReport) []*atlasexec.StepReport {
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
}

func stepIsError(s *atlasexec.StepReport) bool {
	return s.Error != "" || (s.Result != nil && s.Result.Error != "")
}

var (
	//go:embed comments
	comments     embed.FS
	CommentsTmpl = template.Must(
		template.New("comments").
			Funcs(template.FuncMap{
				"execTime":     execTime,
				"appliedStmts": appliedStmts,
				"filterIssues": filterIssues,
				"stepIsError":  stepIsError,
				"stmtsDetected": func(plan *atlasexec.SchemaPlanFile) string {
					switch l := len(plan.Stmts); {
					case l == 0:
						return "No statements detected"
					case l == 1:
						return "1 new statement detected"
					default:
						return fmt.Sprintf("%d new statements detected", l)
					}
				},
				"filesDetected": func(files []*atlasexec.FileReport) string {
					switch l := len(files); {
					case l == 0:
						return "No migration files detected"
					case l == 1:
						return "1 new migration file detected"
					default:
						return fmt.Sprintf("%d new migration files detected", l)
					}
				},
				"fileNames": func(files []*atlasexec.FileReport) []string {
					names := make([]string, len(files))
					for i, f := range files {
						names[i] = f.Name
					}
					return names
				},
				"stepSummary": func(s *atlasexec.StepReport) string {
					if s.Text == "" {
						return s.Name
					}
					return s.Name + "\n" + s.Text
				},
				"stepDetails": func(s *atlasexec.StepReport) string {
					if s.Result == nil {
						return s.Error
					}
					var details []string
					for _, r := range s.Result.Reports {
						if t := r.Text; t != "" {
							details = append(details, fmt.Sprintf("**%s%s**", strings.ToUpper(t[:1]), t[1:]))
						}
						for _, d := range r.Diagnostics {
							if d.Code == "" {
								details = append(details, d.Text)
							} else {
								details = append(details, fmt.Sprintf("%[1]s [(%[2]s)](https://atlasgo.io/lint/analyzers#%[2]s)", d.Text, d.Code))
							}
						}
					}
					return strings.Join(details, "\n")
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
					return fmt.Sprintf("\n\n```%s\n%s\n```\n\n", lang, strings.Trim(code, "\n"))
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
				"nl2br": func(s string) string { return strings.ReplaceAll(s, "\n", "<br/>") },
				"nl2sp": func(s string) string { return strings.ReplaceAll(s, "\n", " ") },
			}).
			ParseFS(comments, "comments/*"),
	)
)

// RenderTemplate renders the given template with the data.
func RenderTemplate(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := CommentsTmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// toEnvName converts the given string to an environment variable name.
func toEnvName(s string) string {
	return strings.ToUpper(strings.NewReplacer(
		" ", "_", "-", "_", "/", "_",
	).Replace(s))
}

// toInputVarName converts the given string to an input variable name.
func toInputVarName(input string) string {
	return fmt.Sprintf("ATLAS_INPUT_%s", toEnvName(input))
}

// toOutputVar converts the given values to an output variable.
// The action and output are used to create the output variable name with the format:
// ATLAS_OUTPUT_<ACTION>_<OUTPUT>="<value>"
func toOutputVar(action, output, value string) string {
	return fmt.Sprintf("ATLAS_OUTPUT_%s=%q", toEnvName(action+"_"+output), value)
}

// fprintln writes the given values to the file using fmt.Fprintln.
func fprintln(name string, val ...any) error {
	// Write the output to a file.
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, val...)
	return err
}

// commentMarker creates a hidden marker to identify the comment as one created by this action.
func commentMarker(id string) string {
	return fmt.Sprintf(`<!-- generated by ariga/atlas-action for %v -->`, id)
}

type coloredLogger struct {
	w io.Writer
}

// Infof implements the Logger interface.
func (l *coloredLogger) Infof(msg string, args ...any) {
	fmt.Fprint(l.w, color.CyanString(msg, args...)+"\n")
}

// Warningf implements the Logger interface.
func (l *coloredLogger) Warningf(msg string, args ...any) {
	fmt.Fprint(l.w, color.YellowString(msg, args...)+"\n")
}

// Errorf implements the Logger interface.
func (l *coloredLogger) Errorf(msg string, args ...any) {
	fmt.Fprint(l.w, color.RedString(msg, args...)+"\n")
}

// Fatalf implements the Logger interface.
func (l *coloredLogger) Fatalf(msg string, args ...any) {
	l.Errorf(msg, args...)
	os.Exit(1)
}

var _ Logger = (*coloredLogger)(nil)
