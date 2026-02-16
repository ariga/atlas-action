// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"ariga.io/atlas-action/internal/teamcity"
	"ariga.io/atlas/atlasexec"
	"github.com/magiconair/properties"
)

type TeamCity struct {
	*teamcity.ServiceMessage
	getenv func(string) string
}

// NewTeamCity creates a new TeamCity action.
func NewTeamCity(getenv func(string) string, w io.Writer) *TeamCity {
	return &TeamCity{teamcity.NewServiceMessage(w), getenv}
}

// Infof implements [Action].
func (t *TeamCity) Infof(msg string, a ...any) {
	t.Message("NORMAL", fmt.Sprintf(msg, a...))
}

// Warningf implements [Action].
func (t *TeamCity) Warningf(msg string, a ...any) {
	t.Message("WARNING", fmt.Sprintf(msg, a...))
}

// Errorf implements [Action].
func (t *TeamCity) Errorf(msg string, a ...any) {
	t.Message("ERROR", fmt.Sprintf(msg, a...))
}

// Fatalf implements [Action].
func (t *TeamCity) Fatalf(msg string, a ...any) {
	t.Message("FAILURE", fmt.Sprintf(msg, a...))
	os.Exit(1) // terminate execution
}

// MigrateApply implements Reporter.
func (t *TeamCity) MigrateApply(_ context.Context, r *atlasexec.MigrateApply) {
	if r == nil {
		return
	}
	const blockName = "atlas migrate apply"
	flowID := teamcity.WithFlowID("migrate-apply")
	t.BlockOpened(blockName, flowID)
	defer t.BlockClosed(blockName, flowID)
	t.Message("NORMAL", fmt.Sprintf("Migration Directory: %s", r.Dir), flowID)
	if r.Current != "" {
		t.Message("NORMAL", fmt.Sprintf("From Version: %s", r.Current), flowID)
	}
	t.Message("NORMAL", fmt.Sprintf("To Version: %s", r.Target), flowID)
	t.Message("NORMAL", fmt.Sprintf("Applied: %d migration file(s)", len(r.Applied)), flowID)
	for _, f := range r.Applied {
		if f.Error != nil {
			t.Message("ERROR", fmt.Sprintf("Migration %s failed: %s", f.Name, f.Error.Text), flowID)
		} else {
			t.Message("NORMAL", fmt.Sprintf("Migration %s applied successfully (%d statements)", f.Name, len(f.Applied)), flowID)
		}
	}
	if r.Error != "" {
		t.Message("ERROR", fmt.Sprintf("Migration failed: %s", r.Error), flowID)
	}
}

// MigrateLint implements Reporter.
func (t *TeamCity) MigrateLint(_ context.Context, r *atlasexec.SummaryReport) {
	if r == nil {
		return
	}
	t.lintReport("atlas migrate lint", r)
}

// SchemaLint implements Reporter.
func (t *TeamCity) SchemaLint(_ context.Context, r *SchemaLintReport) {
	if r == nil || r.SchemaLintReport == nil {
		return
	}
	flowID := teamcity.WithFlowID("schema-lint")
	blockOpts := []teamcity.Option{flowID}
	if desc := strings.TrimSpace(strings.Join(r.URL, ", ")); desc != "" {
		blockOpts = append(blockOpts, teamcity.WithDescription(desc))
	}
	const blockName = "atlas schema lint"
	t.BlockOpened(blockName, blockOpts...)
	defer t.BlockClosed(blockName, flowID)
	if len(r.Steps) == 0 {
		t.Message("NORMAL", "atlas schema lint completed with no findings", flowID)
		return
	}
	const inspectionTypeID = "atlas-schema-lint"
	t.InspectionType(inspectionTypeID, "Atlas Schema Lint", "atlas", "Schema lint checks", flowID)
	for _, step := range r.Steps {
		severity := "WARNING"
		if step.Error {
			severity = "ERROR"
			t.Message("ERROR", fmt.Sprintf("%s %s", step.Text, step.Desc), flowID)
		} else {
			t.Message("WARNING", fmt.Sprintf("%s %s", step.Text, step.Desc), flowID)
		}
		if len(step.Diagnostics) == 0 {
			continue
		}
		for _, diag := range step.Diagnostics {
			// Only report diagnostics with position as TeamCity requires file
			// for inspections.
			// The message of the step will contain the summary of the issue.
			if p := diag.Pos; p != nil {
				msg := fmt.Sprintf(`<a href="https://atlasgo.io/lint/analyzers#%s">%s</a>`,
					diag.Code, diag.Text)
				opts := []teamcity.Option{
					teamcity.WithSeverity(severity),
					teamcity.WithMessage(msg),
					flowID,
				}
				if l := p.Start.Line; l > 0 {
					opts = append(opts, teamcity.WithLine(l))
				}
				t.Inspection(inspectionTypeID, p.Filename, opts...)
			}
		}
	}
}

// SchemaPlan implements Reporter.
func (t *TeamCity) SchemaPlan(_ context.Context, r *atlasexec.SchemaPlan) {
	if r == nil {
		return
	}
	const blockName = "atlas schema plan"
	flowID := teamcity.WithFlowID("schema-plan")
	t.BlockOpened(blockName, flowID)
	defer t.BlockClosed(blockName, flowID)

	if r.Error != "" {
		t.Message("ERROR", fmt.Sprintf("Schema plan failed: %s", r.Error), flowID)
		return
	}
	if r.File != nil {
		t.Message("NORMAL", fmt.Sprintf("Plan: %s", r.File.Name), flowID)
		if r.File.URL != "" {
			t.Message("NORMAL", fmt.Sprintf("URL: %s", r.File.URL), flowID)
		}
		if r.File.Link != "" {
			t.Message("NORMAL", fmt.Sprintf("Link: %s", r.File.Link), flowID)
		}
	}
	// Report lint findings if present
	if r.Lint != nil && len(r.Lint.Files) > 0 {
		t.lintReport("Lint report", r.Lint)
	}
}

// SchemaApply implements Reporter.
func (t *TeamCity) SchemaApply(_ context.Context, r *atlasexec.SchemaApply) {
	if r == nil {
		return
	}
	const blockName = "atlas schema apply"
	flowID := teamcity.WithFlowID("schema-apply")
	t.BlockOpened(blockName, flowID)
	defer t.BlockClosed(blockName, flowID)
	if r.Error != "" {
		t.Message("ERROR", r.Error, flowID)
		return
	}
	if a := r.Applied; a != nil {
		t.Message("NORMAL", fmt.Sprintf("Applied %d statement(s)", len(a.Applied)), flowID)
		if a.Error != nil {
			t.Message("ERROR", fmt.Sprintf("Error in statement: %s", a.Error.Text), flowID)
		}
	}
	// Report plan lint findings if present
	if p := r.Plan; p != nil && p.Lint != nil {
		t.lintReport("Lint report", p.Lint)
	}
}

func (t *TeamCity) lintReport(name string, r *atlasexec.SummaryReport) {
	const typeID = "atlas-lint-report"
	flowID := teamcity.WithFlowID("lint-report")
	t.BlockOpened(name, flowID)
	defer t.BlockClosed(name, flowID)
	if len(r.Files) == 0 {
		t.Message("NORMAL", "Lint report completed with no findings", flowID)
		return
	}
	t.InspectionType(typeID, "Atlas Lint Report", "atlas",
		"Migration lint report", flowID)
	for _, f := range r.Files {
		if f.Error != "" {
			t.Message("ERROR", fmt.Sprintf("File %s: %s", f.Name, f.Error), flowID)
		}
		for _, report := range f.Reports {
			for _, diag := range report.Diagnostics {
				severity := "WARNING"
				if f.Error != "" {
					severity = "ERROR"
				}
				msg := diag.Text
				if diag.Code != "" {
					msg = fmt.Sprintf(`<a href="https://atlasgo.io/lint/analyzers#%s">%s</a>`, diag.Code, diag.Text)
				}
				opts := []teamcity.Option{
					teamcity.WithSeverity(severity),
					teamcity.WithMessage(msg),
					flowID,
				}
				// Calculate line number from position
				if diag.Pos > 0 {
					lines := strings.Split(f.Text[:diag.Pos], "\n")
					opts = append(opts, teamcity.WithLine(max(1, len(lines))))
				}
				t.Inspection(typeID, f.Name, opts...)
			}
		}
	}
}

// GetType implements [Action].
func (t *TeamCity) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerType("TEAMCITY")
}

// GetTriggerContext implements [Action].
func (t *TeamCity) GetTriggerContext(context.Context) (*TriggerContext, error) {
	props, err := t.buildProperties()
	if err != nil {
		return nil, err
	}
	get := func(key string) string {
		return strings.TrimSpace(props.GetString(key, ""))
	}
	tc := &TriggerContext{
		Act:     t,
		Repo:    get("teamcity.projectName"),
		Commit:  get("build.vcs.number"),
		Branch:  get("teamcity.build.branch"),
		RepoURL: get("vcsroot.url"),
	}
	if user := get("teamcity.build.triggeredBy.username"); user != "" {
		tc.Actor = &Actor{Name: user}
	}
	if prNumber := props.GetInt("teamcity.pullRequest.number", 0); prNumber != 0 {
		tc.PullRequest = &PullRequest{
			Number: prNumber,
			Commit: tc.Commit,
		}
	}
	// Detect SCM provider by parsing the URL and checking the hostname
	switch u, err := url.Parse(tc.RepoURL); {
	case err != nil:
		return nil, fmt.Errorf("parsing repo URL %q: %w", tc.RepoURL, err)
	case u.Host != "":
		switch h := strings.ToLower(u.Hostname()); {
		case h == "github.com" || strings.HasSuffix(h, ".github.com"):
			tc.SCMType = atlasexec.SCMTypeGithub
			tc.SCMClient = func() (SCMClient, error) {
				token := t.getenv("GITHUB_TOKEN")
				if token == "" {
					t.Warningf("GITHUB_TOKEN is not set, the action may not have all the permissions")
				}
				return NewGitHubClient(tc.Repo, t.getenv("GITHUB_API_URL"), token)
			}
			if tc.PullRequest != nil {
				tc.PullRequest.URL = fmt.Sprintf("%s/pull/%d", strings.TrimSuffix(tc.RepoURL, ".git"), tc.PullRequest.Number)
			}
		case h == "gitlab.com" || strings.HasSuffix(h, ".gitlab.com"):
			tc.SCMType = atlasexec.SCMTypeGitlab
			tc.SCMClient = func() (SCMClient, error) {
				token := t.getenv("GITLAB_TOKEN")
				if token == "" {
					t.Warningf("GITLAB_TOKEN is not set, the action may not have all the permissions")
				}
				return NewGitLabClient(tc.Repo, t.getenv("CI_API_V4_URL"), token)
			}
			if tc.PullRequest != nil {
				tc.PullRequest.URL = fmt.Sprintf("%s/-/merge_requests/%d", strings.TrimSuffix(tc.RepoURL, ".git"), tc.PullRequest.Number)
			}
		case h == "bitbucket.org":
			tc.SCMType = atlasexec.SCMTypeBitbucket
			tc.SCMClient = func() (SCMClient, error) {
				token := t.getenv("BITBUCKET_ACCESS_TOKEN")
				if token == "" {
					t.Warningf("BITBUCKET_ACCESS_TOKEN is not set, the action may not have all the permissions")
				}
				return NewBitbucketClient(
					t.getenv("BITBUCKET_WORKSPACE"),
					t.getenv("BITBUCKET_REPO_SLUG"),
					token,
				)
			}
			if tc.PullRequest != nil {
				tc.PullRequest.URL = fmt.Sprintf("%s/pull-requests/%d", strings.TrimSuffix(tc.RepoURL, ".git"), tc.PullRequest.Number)
			}
		}
	}
	return tc, nil
}

// Getenv implements [Action].
func (t *TeamCity) Getenv(name string) string {
	return t.getenv(name)
}

// GetInput implements [Action].
func (t *TeamCity) GetInput(name string) string {
	// To pass inputs to the action, define environment variables with the ATLAS_INPUT_ prefix:
	// ```yaml
	// inputs:
	//   - env.ATLAS_INPUT_<name>:
	//       type: text
	//       required: true
	//       label: The label
	//       description: Long description of the input
	// ```
	return t.getenv(toInputVarName(name))
}

// SetOutput implements [Action].
func (t *TeamCity) SetOutput(name string, value string) {
	act := t.getenv("ATLAS_ACTION_COMMAND")
	t.SetParameter(fmt.Sprintf("atlas-action.%s.outputs.%s",
		strings.ReplaceAll(act, "/", "-"), name), value)
	t.SetParameter(fmt.Sprintf("env.%s", toOutputVarName(act, name)), value)
}

func (t *TeamCity) buildProperties() (*properties.Properties, error) {
	path := strings.TrimSpace(t.getenv("TEAMCITY_BUILD_PROPERTIES_FILE"))
	if path == "" {
		return nil, fmt.Errorf("TEAMCITY_BUILD_PROPERTIES_FILE is not set")
	}
	return properties.LoadFile(path, properties.UTF8)
}

var (
	_ Action   = (*TeamCity)(nil)
	_ Reporter = (*TeamCity)(nil)
)
