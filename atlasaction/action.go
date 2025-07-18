// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"ariga.io/atlas-action/atlasaction/cloud"
	"ariga.io/atlas-go-sdk/atlasexec"
	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/sqlclient"
	"github.com/fatih/color"
)

type (
	// Actions holds the runtime for the actions to run.
	// This helps to inject the runtime dependencies. Like the SCM client, Atlas client, etc.
	Actions struct {
		Action
		Version     string
		Atlas       AtlasExec
		CmdExecutor func(ctx context.Context, name string, args ...string) *exec.Cmd
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
		SchemaLint(context.Context, *SchemaLintReport)
	}
	// SCMClient contains methods for interacting with SCM platforms (GitHub, Gitlab etc...).
	SCMClient interface {
		// PullRequest returns information about a pull request.
		PullRequest(context.Context, int) (*PullRequest, error)
		// CreatePullRequest creates a pull request with the given title and body into the given base branch.
		CreatePullRequest(_ context.Context, head, base, title, body string) (*PullRequest, error)
		// CopilotSession returns the Copilot session for the current pull request, if there already is one.
		CopilotSession(context.Context, *TriggerContext) (string, error)
		// CommentCopilot comments on the pull request with the copilot response.
		CommentCopilot(_ context.Context, pr int, _ *Copilot) error
		// CommentLint comments on the pull request with the lint report.
		CommentLint(context.Context, *TriggerContext, *atlasexec.SummaryReport) error
		// CommentPlan comments on the pull request with the schema plan.
		CommentPlan(context.Context, *TriggerContext, *atlasexec.SchemaPlan) error
		// CommentSchemaLint comments on the pull request with the schema lint report.
		CommentSchemaLint(context.Context, *TriggerContext, *SchemaLintReport) error
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
		// CopilotStream runs the 'copilot' command in one-shot mode, streaming the messages.
		CopilotStream(context.Context, *atlasexec.CopilotParams) (atlasexec.Stream[*atlasexec.CopilotMessage], error)
		// MigrateStatus runs the `migrate status` command.
		MigrateStatus(context.Context, *atlasexec.MigrateStatusParams) (*atlasexec.MigrateStatus, error)
		// MigrateHash runs the `migrate hash` command.
		MigrateHash(context.Context, *atlasexec.MigrateHashParams) error
		// MigrateDiff runs the `migrate diff --dry-run` command.
		MigrateDiff(ctx context.Context, params *atlasexec.MigrateDiffParams) (*atlasexec.MigrateDiff, error)
		// MigrateRebase runs the `migrate rebase` command.
		MigrateRebase(context.Context, *atlasexec.MigrateRebaseParams) error
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
		// SchemaLint runs the `schema lint` command.
		SchemaLint(context.Context, *atlasexec.SchemaLintParams) (*atlasexec.SchemaLintReport, error)
		// SchemaPlanList runs the `schema plan list` command.
		SchemaPlanList(context.Context, *atlasexec.SchemaPlanListParams) ([]atlasexec.SchemaPlanFile, error)
		// SchemaPlanLint runs the `schema plan lint` command.
		SchemaPlanLint(context.Context, *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error)
		// SchemaPlanApprove runs the `schema plan approve` command.
		SchemaPlanApprove(context.Context, *atlasexec.SchemaPlanApproveParams) (*atlasexec.SchemaPlanApprove, error)
		// SchemaApplySlice runs the `schema apply` command.
		SchemaApplySlice(context.Context, *atlasexec.SchemaApplyParams) ([]*atlasexec.SchemaApply, error)
		// WhoAmI runs the `whoami` command.
		WhoAmI(context.Context, *atlasexec.WhoAmIParams) (*atlasexec.WhoAmI, error)
		// SetStderr sets the standard error output for the client.
		SetStderr(io.Writer)
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
		Act           Action                    // Act is the action that is running.
		SCMType       atlasexec.SCMType         // Type of the SCM, e.g. "GITHUB" / "GITLAB" / "BITBUCKET".
		SCMClient     func() (SCMClient, error) // SCMClient returns a SCMClient for the current action.
		Repo          string                    // Repo is the repository name. e.g. "ariga/atlas-action".
		RepoURL       string                    // RepoURL is full URL of the repository. e.g. "https://github.com/ariga/atlas-action".
		DefaultBranch string                    // DefaultBranch is the default branch of the repository.
		Branch        string                    // Current Branch name.
		Commit        string                    // Commit SHA.
		Actor         *Actor                    // Actor is the user who triggered the action.
		RerunCmd      string                    // RerunCmd is the command to rerun the action.

		PullRequest *PullRequest // PullRequest will be available if the event is "pull_request".
		Comment     *Comment     // Comment will be available if the event is "issue_comment".
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
		Body   string // Body (description) of the pull request.
		Commit string // Latest commit SHA.
		Ref    string
	}
	// Comment holds the comment information.
	Comment struct {
		Number int    // Pull Request Number
		URL    string // URL of the comment, e.g "https://github.com/ariga/atlas-action/pull/1#issuecomment-1234567890"
		Body   string // Body (description) of the comment
	}
	SchemaLintReport struct {
		URL []string `json:"URL,omitempty"` // Redacted schema URLs
		*atlasexec.SchemaLintReport
	}
	// Copilot contains both the prompt and the response from Atlas Copilot.
	Copilot struct {
		Session, Prompt, Response string
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
		CmdExecutor: cfg.CmdExecutor,
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
			c.action = NewGitHub(c.getenv, c.out)
			// Forward all output from stderr to the action logger as warnings.
			c.atlas.SetStderr(logWriter(c.action.Warningf))
		case c.getenv("CIRCLECI") == "true":
			c.action = NewCircleCI(c.getenv, c.out)
		case c.getenv("GITLAB_CI") == "true":
			c.action = NewGitlab(c.getenv, c.out)
		case c.getenv("BITBUCKET_PIPELINE_UUID") != "":
			c.action = NewBitBucket(c.getenv, c.out)
		case c.getenv("TF_BUILD") == "True":
			c.action = NewAzure(c.getenv, c.out)
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

// WithCmdExecutor specifies how to execute commands.
func WithCmdExecutor(exec func(ctx context.Context, name string, args ...string) *exec.Cmd) Option {
	return func(c *config) { c.CmdExecutor = exec }
}

type (
	config struct {
		getenv      func(string) string
		out         io.Writer
		action      Action
		atlas       AtlasExec
		CmdExecutor func(context.Context, string, ...string) *exec.Cmd
		cloudClient func(string, string, *atlasexec.Version) CloudClient
		version     string
		err         error // the error occurred during the configuration.
	}
	Option func(*config)
)

const (
	// Versioned workflow Commands
	CmdMigratePush       = "migrate/push"
	CmdMigrateLint       = "migrate/lint"
	CmdMigrateApply      = "migrate/apply"
	CmdMigrateDown       = "migrate/down"
	CmdMigrateTest       = "migrate/test"
	CmdMigrateAutoRebase = "migrate/autorebase"
	CmdMigrateDiff       = "migrate/diff"
	// Declarative workflow Commands
	CmdSchemaPush        = "schema/push"
	CmdSchemaLint        = "schema/lint"
	CmdSchemaTest        = "schema/test"
	CmdSchemaPlan        = "schema/plan"
	CmdSchemaPlanApprove = "schema/plan/approve"
	CmdSchemaApply       = "schema/apply"
	// Monitoring Commands
	CmdMonitorSchema = "monitor/schema"
	// Copilot Commands
	CmdCopilot = "copilot"
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
	case CmdMigrateAutoRebase:
		return a.MigrateAutoRebase(ctx)
	case CmdMigrateDiff:
		return a.MigrateDiff(ctx)
	case CmdSchemaPush:
		return a.SchemaPush(ctx)
	case CmdSchemaLint:
		return a.SchemaLint(ctx)
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
	case CmdCopilot:
		return a.Copilot(ctx)
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
		Amount:          a.GetUin64Input("amount"),
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
	var run *atlasexec.MigrateDown
	printed := false
	if err := a.waitingForApproval(func() (bool, error) {
		run, err = a.Atlas.MigrateDown(ctx, params)
		if err != nil {
			a.SetOutput("error", err.Error())
			return false, err
		}
		if run.Error != "" {
			a.SetOutput("error", run.Error)
			return false, errors.New(run.Error)
		}
		if run.Status != StatePending {
			return true, nil
		}
		if !printed {
			printed = true
			a.Infof("plan approval pending, review here: %s", run.URL)
		}
		return false, nil
	}); err != nil {
		if !errors.Is(err, ErrApprovalTimeout) {
			return err
		}
		a.Warningf("plan has not been approved in configured waiting period, exiting")
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
	a.Infof(`"atlas migrate push" completed successfully, pushed directory %q to Atlas Cloud`, params.Name)
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
		c, err := tc.SCMClient()
		if err != nil {
			return err
		}
		if err = c.CommentLint(ctx, tc, &payload); err != nil {
			a.Errorf("failed to comment on the pull request: %v", err)
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
		Paths:           a.GetArrayInput("paths"),
	})
	if err != nil {
		return fmt.Errorf("`atlas migrate test` completed with errors:\n%s", err)
	}
	a.Infof("`atlas migrate test` completed successfully, no issues found")
	a.Infof(result)
	return nil
}

// MigrateAutoRebase runs the Action for "ariga/atlas-action/migrate/autorebase"
func (a *Actions) MigrateAutoRebase(ctx context.Context) error {
	tc, err := a.GetTriggerContext(ctx)
	if err != nil {
		return err
	}
	var (
		remote     = a.GetInputDefault("remote", "origin")
		baseBranch = a.GetInputDefault("base-branch", tc.DefaultBranch)
		currBranch = tc.Branch
	)
	if v, err := a.exec(ctx, "git", "--version"); err != nil {
		return fmt.Errorf("failed to get git version: %w", err)
	} else {
		a.Infof("auto-rebase with %s", v)
	}
	if _, err := a.exec(ctx, "git", "fetch", remote, baseBranch); err != nil {
		return fmt.Errorf("failed to fetch the branch %s: %w", baseBranch, err)
	}
	// Since running in detached HEAD, we need to switch to the branch.
	if _, err := a.exec(ctx, "git", "checkout", currBranch); err != nil {
		return fmt.Errorf("failed to checkout to the branch: %w", err)
	}
	dirURL := a.GetInputDefault("dir", "file://migrations")
	u, err := url.Parse(dirURL)
	if err != nil {
		return fmt.Errorf("failed to parse dir URL: %w", err)
	}
	dirPath := filepath.Join(u.Host, u.Path)
	sumPath := filepath.Join(a.WorkingDir(), dirPath, migrate.HashFileName)
	baseHash, err := a.hashFileFrom(ctx, remote, baseBranch, sumPath)
	if err != nil {
		return fmt.Errorf("failed to get the atlas.sum file from the base branch: %w", err)
	}
	currHash, err := a.hashFileFrom(ctx, remote, currBranch, sumPath)
	if err != nil {
		return fmt.Errorf("failed to get the atlas.sum file from the current branch: %w", err)
	}
	files := newFiles(baseHash, currHash)
	if len(files) == 0 {
		a.Infof("No new migration files to rebase")
		return nil
	}
	// Try to merge the base branch into the current branch.
	if _, err := a.exec(ctx, "git", "merge", "--no-ff",
		fmt.Sprintf("%s/%s", remote, baseBranch)); err == nil {
		a.Infof("No conflict found when merging %s into %s", baseBranch, currBranch)
		return nil
	}
	// If merge failed due to conflict, check that the conflict is only in atlas.sum file.
	switch out, err := a.exec(ctx, "git", "diff", "--name-only", "--diff-filter=U"); {
	case err != nil:
		return fmt.Errorf("failed to get conflicting files: %w", err)
	case len(out) == 0:
		return errors.New("conflict found but no conflicting files found")
	case strings.TrimSpace(string(out)) != sumPath:
		a.Infof("Conflict files are:\n%s", out)
		return fmt.Errorf("conflict found in files other than %s", sumPath)
	}
	// Re-hash the migrations and rebase the migrations.
	if err = a.Atlas.MigrateHash(ctx, &atlasexec.MigrateHashParams{
		DirURL: dirURL,
	}); err != nil {
		return fmt.Errorf("failed to run `atlas migrate hash`: %w", err)
	}
	if err = a.Atlas.MigrateRebase(ctx, &atlasexec.MigrateRebaseParams{
		DirURL: dirURL,
		Files:  files,
	}); err != nil {
		return fmt.Errorf("failed to rebase migrations: %w", err)
	}
	if _, err = a.exec(ctx, "git", "add", dirPath); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}
	if _, err = a.exec(ctx, "git", "commit", "--message",
		fmt.Sprintf("%s: rebase migration files", dirPath)); err != nil {
		return fmt.Errorf("failed to commit changes: %w", err)
	}
	if _, err = a.exec(ctx, "git", "push", remote, currBranch); err != nil {
		return fmt.Errorf("failed to push changes: %w", err)
	}
	a.Infof("Migrations rebased successfully")
	return nil
}

// MigrateDiff runs the GitHub Action for "ariga/atlas-action/migrate/diff"
func (a *Actions) MigrateDiff(ctx context.Context) error {
	tc, err := a.GetTriggerContext(ctx)
	if err != nil {
		return err
	}
	var (
		remote     = a.GetInputDefault("remote", "origin")
		currBranch = tc.Branch
	)
	params := &atlasexec.MigrateDiffParams{
		DirURL:    a.GetInput("dir"),
		ToURL:     a.GetInput("to"),
		DevURL:    a.GetInput("dev-url"),
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
	}
	diff, err := a.Atlas.MigrateDiff(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to run `atlas migrate diff`: %w", err)
	}
	if len(diff.Files) == 0 {
		a.Infof("The migration directory is synced with the desired state, no changes to be made")
		return nil
	}
	if v, err := a.exec(ctx, "git", "--version"); err != nil {
		return fmt.Errorf("failed to get git version: %w", err)
	} else {
		a.Infof("migrate diff with %s", v)
	}
	if _, err := a.exec(ctx, "git", "fetch", remote, currBranch); err != nil {
		return fmt.Errorf("failed to fetch the branch %s: %w", currBranch, err)
	}
	// Since running in detached HEAD, we need to switch to the branch.
	if _, err := a.exec(ctx, "git", "checkout", currBranch); err != nil {
		return fmt.Errorf("failed to checkout to the branch: %w", err)
	}
	// If there is a diff, add the files to the migration directory, run `migrate hash` commit and push the changes.
	u, err := url.Parse(diff.Dir)
	if err != nil {
		return fmt.Errorf("failed to parse dir URL: %w", err)
	}
	dirPath := filepath.Join(u.Host, u.Path)
	for _, f := range diff.Files {
		if err := os.WriteFile(filepath.Join(dirPath, f.Name), []byte(f.Content), 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", f.Name, err)
		}
	}
	if err = a.Atlas.MigrateHash(ctx, &atlasexec.MigrateHashParams{
		DirURL: diff.Dir,
	}); err != nil {
		return fmt.Errorf("failed to run `atlas migrate hash`: %w", err)
	}
	// Add the new migration files to the git index and commit the changes.
	if _, err = a.exec(ctx, "git", "add", dirPath); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}
	if _, err = a.exec(ctx, "git", "commit", "--message",
		fmt.Sprintf("%s: add new migration file", dirPath)); err != nil {
		return fmt.Errorf("failed to commit changes: %w", err)
	}
	if _, err = a.exec(ctx, "git", "push", remote, currBranch); err != nil {
		return fmt.Errorf("failed to push changes: %w", err)
	}
	a.Infof("Run migrate/diff completed successfully")
	return nil
}

// hashFileFrom returns the hash file from the remote branch.
func (a *Actions) hashFileFrom(ctx context.Context, remote, branch, path string) (migrate.HashFile, error) {
	data, err := a.exec(ctx, "git", "show",
		fmt.Sprintf("%s/%s:%s", remote, branch, path))
	if err != nil {
		return nil, err
	}
	var hf migrate.HashFile
	if err := hf.UnmarshalText(data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal atlas.sum: %w", err)
	}
	return hf, nil
}

// exec runs the command and returns the output.
func (a *Actions) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := a.CmdExecutor(ctx, name, args...)
	out, err := cmd.Output()
	switch err := err.(type) {
	case nil:
		return out, nil
	case *exec.ExitError:
		if err.Stderr != nil {
			a.Infof("Running %q got following error: %s", cmd.String(), string(err.Stderr))
		}
		return nil, fmt.Errorf("failed to run %s: %w", name, err)
	default:
		return nil, fmt.Errorf("failed to run %s: %w", name, err)
	}
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

// SchemaLint runs the GitHub Action for "ariga/atlas-action/schema/lint"
func (a *Actions) SchemaLint(ctx context.Context) error {
	tc, err := a.GetTriggerContext(ctx)
	switch {
	case err != nil:
		return fmt.Errorf("unable to get the trigger context: %w", err)
	}
	params := &atlasexec.SchemaLintParams{
		ConfigURL: a.GetInput("config"),
		Vars:      a.GetVarsInput("vars"),
		Env:       a.GetInput("env"),
		URL:       a.GetArrayInput("url"),
		Schema:    a.GetArrayInput("schema"),
		DevURL:    a.GetInput("dev-url"),
	}
	report, err := a.Atlas.SchemaLint(ctx, params)
	if err != nil {
		a.SetOutput("error", err.Error())
		return fmt.Errorf("`atlas schema lint` completed failed with errors:\n%s", err)
	}
	if len(report.Steps) == 0 {
		a.Infof("`atlas schema lint` completed successfully, no issues found")
		return nil
	}
	redactedURLs := make([]string, 0, len(params.URL))
	for _, u := range params.URL {
		redacted, err := redactedURL(u)
		if err != nil {
			a.Errorf("failed to redact URL: %v", err)
		} else {
			redactedURLs = append(redactedURLs, redacted)
		}
	}
	rp := &SchemaLintReport{
		URL:              redactedURLs,
		SchemaLintReport: report,
	}
	if r, ok := a.Action.(Reporter); ok {
		r.SchemaLint(ctx, rp)
	}
	if tc.PullRequest != nil {
		c, err := tc.SCMClient()
		if err != nil {
			a.Errorf("failed to get SCM client: %v", err)
		} else if err = c.CommentSchemaLint(ctx, tc, rp); err != nil {
			a.Errorf("failed to comment on the pull request: %v", err)
		}
	}
	errorCount, warningCount := 0, 0
	for _, step := range report.Steps {
		if step.Error {
			errorCount++
		} else {
			warningCount++
		}
	}
	if errorCount > 0 {
		return fmt.Errorf("`atlas schema lint` completed successfully with %d errors and %d warnings, check the annotations for details", errorCount, warningCount)
	}
	a.Infof("`atlas schema lint` completed successfully with %d warnings, check the annotations for details", warningCount)
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
		Paths:     a.GetArrayInput("paths"),
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
		Include:   a.GetArrayInput("include"),
		Exclude:   a.GetArrayInput("exclude"),
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
			Include:   params.Include,
			Exclude:   params.Exclude,
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
			Include:    params.Include,
			Exclude:    params.Exclude,
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
	c, err := tc.SCMClient()
	if err != nil {
		return err
	}
	if err = c.CommentPlan(ctx, tc, plan); err != nil {
		// Don't fail the action if the comment fails.
		// It may be due to the missing permissions.
		a.Errorf("failed to comment on the pull request: %v", err)
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
			Include:   a.GetArrayInput("include"),
			Exclude:   a.GetArrayInput("exclude"),
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
func (a *Actions) SchemaApply(ctx context.Context) (err error) {
	var results []*atlasexec.SchemaApply
	// Determine if the approval process should be used.
	useApproval :=
		a.GetInput("lint-review") != "" &&
			!a.GetBoolInput("auto-approve") &&
			!a.GetBoolInput("dry-run") &&
			a.GetInput("plan") == ""
	if useApproval {
		results, err = a.schemaApplyWithApproval(ctx)
	} else {
		results, err = a.Atlas.SchemaApplySlice(ctx, a.schemaApplyParams())
	}
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

// schemaApplyWithApproval applies schema changes with an approval process.
// It waits for the schema plan to be approved before applying the changes.
func (a *Actions) schemaApplyWithApproval(ctx context.Context) ([]*atlasexec.SchemaApply, error) {
	tc, err := a.GetTriggerContext(ctx)
	if err != nil {
		return nil, err
	}
	// Extract repo name from the URL it's provided. e.g. "atlas://repo-name?tag=tag-name"
	u, err := a.GetURLInput("to")
	if err != nil {
		return nil, err
	}
	var repo string
	if u.Scheme == "atlas" {
		repo = fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	}
	// Wait for the schema plan to be approved and apply the changes.
	printed := false
	waitAndApplyPlan := func(f *atlasexec.SchemaPlanFile) ([]*atlasexec.SchemaApply, error) {
		if err := a.waitingForApproval(func() (bool, error) {
			plans, err := a.Atlas.SchemaPlanList(ctx, a.schemaPlanListParams(
				func(p *atlasexec.SchemaPlanListParams) {
					p.From = a.GetArrayInput("url")
					p.To = a.GetArrayInput("to")
					p.Repo = repo
				},
			))
			if err != nil {
				return false, err
			}
			// Check the created plan exists and is approved.
			var cloudPlan *atlasexec.SchemaPlanFile
			for _, plan := range plans {
				if plan.URL == f.URL {
					cloudPlan = &plan
					break
				}
			}
			if cloudPlan == nil {
				return false, errors.New("schema plan not found")
			}
			if cloudPlan.Status == "APPROVED" {
				return true, nil
			}
			if !printed {
				printed = true
				a.Warningf("Schema plan is pending approval, review here: %s", f.Link)
			}
			return false, nil
		}); err != nil {
			if err == ErrApprovalTimeout {
				return nil, fmt.Errorf(
					`the schema plan %q was not approved within the specified waiting period. Please review the plan and re-run the action.
You can approve the plan by visiting: %s`, f.Name, f.Link)
			}
			return nil, err
		}
		return a.Atlas.SchemaApplySlice(ctx, a.schemaApplyParams(
			func(p *atlasexec.SchemaApplyParams) {
				p.PlanURL = f.URL
			},
		))
	}
	// Create a new approval plan
	createApprovalPlan := func() ([]*atlasexec.SchemaApply, error) {
		hash := generateRandomHash()
		// Build plan name based on the pull request number or commit SHA.
		// Commit SHA is used as a fallback if user setups the action to run on push event.
		name := fmt.Sprintf("commit-%.8s-%s", tc.Commit, hash)
		if tc.PullRequest != nil {
			name = fmt.Sprintf("pr-%d-%s", tc.PullRequest.Number, hash)
		}
		plan, err := a.Atlas.SchemaPlan(ctx, a.schemaPlanParams(
			func(p *atlasexec.SchemaPlanParams) {
				p.From = a.GetArrayInput("url")
				p.To = a.GetArrayInput("to")
				p.Repo = repo
				p.Name = name
				p.Pending = true
			},
		))
		if err != nil {
			if strings.Contains(err.Error(), "The current state is synced with the desired state, no changes to be made") {
				// Nothing to do.
				a.Infof("The current state is synced with the desired state, no changes to be made")
				return nil, nil
			}
			return nil, fmt.Errorf("failed to create schema plan: %w", err)
		}
		return waitAndApplyPlan(plan.File)
	}
	// Check existing plans and decide to create a new plan.
	review := a.GetInput("lint-review")
	switch plans, err := a.Atlas.SchemaPlanList(ctx, a.schemaPlanListParams(
		func(p *atlasexec.SchemaPlanListParams) {
			p.From = a.GetArrayInput("url")
			p.To = a.GetArrayInput("to")
			p.Repo = repo
		},
	)); {
	case err != nil:
		return nil, fmt.Errorf("failed to list schema plans: %w", err)
	// Let Atlas decide what to do with the existing plans.
	case len(plans) > 1:
		return a.Atlas.SchemaApplySlice(ctx, a.schemaApplyParams())
	// There are no pending plans or the policy is set to "ALWAYS". Create a new plan.
	case len(plans) == 0 && review == "ALWAYS":
		return createApprovalPlan()
	// There are no pending plans and the review policy is set to "WARNING" or "ERROR".
	// In this case, we need plan the changes and check for errors.
	case len(plans) == 0 && (review == "WARNING" || review == "ERROR"):
		plan, err := a.Atlas.SchemaPlan(ctx, a.schemaPlanParams(
			func(p *atlasexec.SchemaPlanParams) {
				p.From = a.GetArrayInput("url")
				p.To = a.GetArrayInput("to")
				p.Repo = repo
				p.DryRun = true
			},
		))
		if err != nil {
			return nil, fmt.Errorf("failed to plan schema changes: %w", err)
		}
		if plan.Lint == nil {
			return a.Atlas.SchemaApplySlice(ctx, a.schemaApplyParams())
		}
		needApproval := false
		switch review {
		case "WARNING":
			needApproval = plan.Lint.DiagnosticsCount() > 0
		case "ERROR":
			needApproval = len(plan.Lint.Errors()) > 0
		}
		if !needApproval {
			return a.Atlas.SchemaApplySlice(ctx, a.schemaApplyParams())
		}
		return createApprovalPlan()
	case len(plans) == 1:
		return waitAndApplyPlan(&plans[0])
	default:
		return nil, errors.New("unexpected state")
	}
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

// ErrApprovalTimeout is returned when the timeout is exceeded in the approval process.
var ErrApprovalTimeout = errors.New("approval process timed out")

// waitingForApproval waits for the plan to be approved.
func (a *Actions) waitingForApproval(isStop func() (bool, error)) error {
	// Based on the retry configuration values, retry the action if there is an error.
	var (
		interval = a.GetDurationInput("wait-interval")
		timeout  = a.GetDurationInput("wait-timeout")
	)
	if interval == 0 {
		interval = time.Second // Default interval is 1 second.
	}
	for started := time.Now(); ; {
		stop, err := isStop()
		if err != nil {
			return err
		}
		if stop || timeout == 0 {
			break
		}
		if time.Since(started) >= timeout {
			return ErrApprovalTimeout
		}
		time.Sleep(interval)
	}
	return nil
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
// paramsName is List of input names to be added as query parameters.
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

// GetInputDefault returns the input with the given name.
// If the input is empty, it returns the default value.
func (a *Actions) GetInputDefault(name, def string) string {
	if v := a.GetInput(name); v != "" {
		return v
	}
	return def
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

// schemaApplyParams returns the parameters for the schema apply action based on the inputs.
func (a *Actions) schemaApplyParams(withParams ...func(*atlasexec.SchemaApplyParams)) *atlasexec.SchemaApplyParams {
	params := &atlasexec.SchemaApplyParams{
		ConfigURL:   a.GetInput("config"),
		Env:         a.GetInput("env"),
		Vars:        a.GetVarsInput("vars"),
		DevURL:      a.GetInput("dev-url"),
		URL:         a.GetInput("url"),
		To:          a.GetInput("to"),
		Schema:      a.GetArrayInput("schema"),
		Include:     a.GetArrayInput("include"),
		Exclude:     a.GetArrayInput("exclude"),
		DryRun:      a.GetBoolInput("dry-run"),
		AutoApprove: a.GetBoolInput("auto-approve"),
		PlanURL:     a.GetInput("plan"),
		TxMode:      a.GetInput("tx-mode"), // Hidden param.
	}
	for _, f := range withParams {
		f(params)
	}
	return params
}

// schemaPlanListParams returns the parameters for the schema plan list action based on the inputs.
func (a *Actions) schemaPlanListParams(
	withParams ...func(*atlasexec.SchemaPlanListParams),
) *atlasexec.SchemaPlanListParams {
	params := &atlasexec.SchemaPlanListParams{
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
		Repo:      a.GetInput("repo"),
		DevURL:    a.GetInput("dev-url"),
		Schema:    a.GetArrayInput("schema"),
		From:      a.GetArrayInput("from"),
		To:        a.GetArrayInput("to"),
	}
	for _, f := range withParams {
		f(params)
	}
	return params
}

// schemaPlanParams returns the parameters for the schema plan action based on the inputs.
func (a *Actions) schemaPlanParams(
	withParams ...func(*atlasexec.SchemaPlanParams),
) *atlasexec.SchemaPlanParams {
	params := &atlasexec.SchemaPlanParams{
		ConfigURL: a.GetInput("config"),
		Env:       a.GetInput("env"),
		Vars:      a.GetVarsInput("vars"),
		Schema:    a.GetArrayInput("schema"),
		From:      a.GetArrayInput("from"),
		To:        a.GetArrayInput("to"),
		DevURL:    a.GetInput("dev-url"),
		Repo:      a.GetInput("repo"),
		Name:      a.GetInput("name"),
	}
	for _, f := range withParams {
		f(params)
	}
	return params
}

// GetRunContext returns the run context for the action.
func (tc *TriggerContext) GetRunContext() *atlasexec.RunContext {
	rc := &atlasexec.RunContext{
		URL:     tc.RepoURL,
		Repo:    tc.Repo,
		Branch:  tc.Branch,
		Commit:  tc.Commit,
		SCMType: tc.SCMType,
	}
	if pr := tc.PullRequest; pr != nil {
		rc.URL = pr.URL
	}
	if a := tc.Actor; a != nil {
		rc.Username, rc.UserID = a.Name, a.ID
	}
	return rc
}

// newFiles returns the files that only exists in the current hash.
func newFiles(base, current migrate.HashFile) []string {
	m := maps.Collect(hashIter(current))
	for k := range hashIter(base) {
		delete(m, k)
	}
	return slices.Collect(maps.Keys(m))
}

func hashIter(hf migrate.HashFile) iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for _, v := range hf {
			if !yield(v.N, v.H) {
				return
			}
		}
	}
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
				"repoLink": func(planLink string) string {
					// Extract repository link from plan link
					// e.g. "https://ariga-atlas.atlasgo.cloud/schemas/1/plans/2"
					// becomes "https://ariga-atlas.atlasgo.cloud/schemas/1"
					if i := strings.LastIndex(planLink, "/plans/"); i != -1 {
						return planLink[:i]
					}
					return planLink
				},
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
	return "ATLAS_INPUT_" + toEnvName(input)
}

// ToInputVarName converts the given string to an input variable name.
func toOutputVarName(action, output string) string {
	return "ATLAS_OUTPUT_" + toEnvName(action+"_"+output)
}

// toOutputVar converts the given values to an output variable.
// The action and output are used to create the output variable name with the format:
// ATLAS_OUTPUT_<ACTION>_<OUTPUT>="<value>"
func toOutputVar(action, output, value string) string {
	return fmt.Sprintf("%s=%q", toOutputVarName(action, output), value)
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

func redactedURL(s string) (string, error) {
	u, err := sqlclient.ParseURL(s)
	if err != nil {
		return "", err
	}
	return u.Redacted(), nil
}

// commentMarker creates a hidden marker to identify the comment as one created by this action.
func commentMarker(id string) string {
	return fmt.Sprintf(`<!-- generated by ariga/atlas-action for %v -->`, id)
}

// generateRandomHash generates a random 32-bit hash.
func generateRandomHash() string {
	n := make([]byte, 4)
	_, err := rand.Read(n)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%08x", n)
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

type logWriter func(string, ...any)

var _ io.Writer = (*logWriter)(nil)

// Write implements the io.Writer interface for logWriter.
// It writes p to the log using the logWriter function.
func (s logWriter) Write(p []byte) (int, error) {
	s("%s", string(p))
	return len(p), nil
}
