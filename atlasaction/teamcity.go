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
	"time"

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
	// Calculate statistics for tracking.
	appliedStmts := 0
	for _, f := range r.Applied {
		appliedStmts += len(f.Applied)
	}
	duration := durationMs(r.Start, r.End)
	// Report build statistics for trending charts.
	t.BuildStatisticValue("atlas.migrate.apply.files", fmt.Sprintf("%d", len(r.Applied)))
	t.BuildStatisticValue("atlas.migrate.apply.statements", fmt.Sprintf("%d", appliedStmts))
	if duration > 0 {
		t.BuildStatisticValue("atlas.migrate.apply.duration.ms", fmt.Sprintf("%d", duration))
	}
	// Add build tags for filtering builds.
	switch {
	case r.Error != "":
		t.AddBuildTag("migration-failed")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, migration failed at %s", r.Target))
		t.BuildProblem(r.Error, teamcity.WithIdentity(fmt.Sprintf("atlas-migrate-%s", r.Target)))
	case len(r.Applied) > 0:
		t.AddBuildTag("migration-applied")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, migrated to %s (%d file(s), %d statement(s))",
			r.Target, len(r.Applied), appliedStmts))
	default:
		t.AddBuildTag("migration-noop")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, no migrations to apply (at %s)", r.Target))
	}
	// Detailed block output for build log.
	const blockName = "atlas migrate apply"
	flowID := teamcity.WithFlowID("migrate-apply")
	desc := fmt.Sprintf("%s -> %s", r.Current, r.Target)
	if r.Current == "" {
		desc = fmt.Sprintf("-> %s", r.Target)
	}
	t.BlockOpened(blockName, flowID, teamcity.WithDescription(desc))
	defer t.BlockClosed(blockName, flowID)
	t.Message("NORMAL", fmt.Sprintf("Migration Directory: %s", r.Dir), flowID)
	if r.Current != "" {
		t.Message("NORMAL", fmt.Sprintf("From Version: %s", r.Current), flowID)
	}
	t.Message("NORMAL", fmt.Sprintf("To Version: %s", r.Target), flowID)
	if duration > 0 {
		t.Message("NORMAL", fmt.Sprintf("Duration: %dms", duration), flowID)
	}
	t.Message("NORMAL", fmt.Sprintf("Applied: %d migration file(s), %d statement(s)", len(r.Applied), appliedStmts), flowID)
	for _, f := range r.Applied {
		hasTime := !f.Start.IsZero() && !f.End.IsZero()
		fileDuration := durationMs(f.Start, f.End)
		if f.Error != nil {
			if hasTime {
				t.Message("ERROR", fmt.Sprintf("Migration %s failed in %dms: %s", f.Name, fileDuration, f.Error.Text), flowID)
			} else {
				t.Message("ERROR", fmt.Sprintf("Migration %s failed: %s", f.Name, f.Error.Text), flowID)
			}
		} else {
			if hasTime {
				t.Message("NORMAL", fmt.Sprintf("Migration %s applied successfully (%d statements in %dms)", f.Name, len(f.Applied), fileDuration), flowID)
			} else {
				t.Message("NORMAL", fmt.Sprintf("Migration %s applied successfully (%d statements)", f.Name, len(f.Applied)), flowID)
			}
		}
	}
	if r.Error != "" {
		t.Message("ERROR", fmt.Sprintf("Migration failed: %s", r.Error), flowID)
	}
}

// MigrateLint implements Reporter.
func (t *TeamCity) MigrateLint(_ context.Context, r *atlasexec.SummaryReport) {
	t.lintReport(r, "lint")
}

// SchemaLint implements Reporter.
func (t *TeamCity) SchemaLint(_ context.Context, r *SchemaLintReport) {
	if r == nil || r.SchemaLintReport == nil {
		return
	}
	var totalDiags, totalErrors int
	var errorDetails []string
	for _, s := range r.Steps {
		totalDiags += len(s.Diagnostics)
		if s.Error {
			totalErrors++
			if s.Text != "" {
				errorDetails = append(errorDetails, s.Text)
			}
		}
	}
	// Report build statistics for trending charts.
	t.BuildStatisticValue("atlas.schema.lint.steps", fmt.Sprintf("%d", len(r.Steps)))
	t.BuildStatisticValue("atlas.schema.lint.diagnostics", fmt.Sprintf("%d", totalDiags))
	t.BuildStatisticValue("atlas.schema.lint.errors", fmt.Sprintf("%d", totalErrors))
	// Add build tags for filtering builds.
	switch {
	case totalErrors > 0:
		t.AddBuildTag("schema-lint-failed")
		summary := strings.Join(errorDetails, "; ")
		if len(summary) > 80 {
			summary = summary[:77] + "..."
		}
		t.BuildStatus(fmt.Sprintf("{build.status.text}, schema lint failed: %s", summary))
		t.BuildProblem(fmt.Sprintf("Schema lint found %d error(s)", totalErrors),
			teamcity.WithIdentity("atlas-schema-lint"))
	case totalDiags > 0:
		t.AddBuildTag("schema-lint-issues")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, schema lint found %d issue(s) in %d step(s)", totalDiags, len(r.Steps)))
	default:
		t.AddBuildTag("schema-lint-clean")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, schema lint passed (%d step(s))", len(r.Steps)))
	}
	// Detailed block output for build log.
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
		switch {
		case step.Error:
			severity = "ERROR"
			t.Message("ERROR", fmt.Sprintf("%s %s", step.Text, step.Desc), flowID)
		case len(step.Diagnostics) > 0:
			t.Message("WARNING", fmt.Sprintf("%s %s", step.Text, step.Desc), flowID)
		default:
			t.Message("NORMAL", fmt.Sprintf("%s %s", step.Text, step.Desc), flowID)
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
	t.planReport(r, "schema-plan-lint")
}

// SchemaApply implements Reporter.
func (t *TeamCity) SchemaApply(_ context.Context, r *atlasexec.SchemaApply) {
	if r == nil {
		return
	}
	if p := r.Plan; p != nil {
		t.planReport(p, "schema-apply-lint")
		if p.File != nil {
			t.AddBuildTag("schema-apply-pre-planned")
		}
	}
	durationMs := durationMs(r.Start, r.End)
	if durationMs > 0 {
		t.BuildStatisticValue("atlas.schema.apply.duration.ms", fmt.Sprintf("%d", durationMs))
	}
	appliedStmts, hasError := 0, r.Error != ""
	if a := r.Applied; a != nil {
		appliedStmts = len(a.Applied)
		if a.Error != nil {
			hasError = true
		}
	}
	t.BuildStatisticValue("atlas.schema.apply.statements", fmt.Sprintf("%d", appliedStmts))
	switch {
	case r.Error != "":
		t.AddBuildTag("schema-apply-failed")
		t.BuildStatus("{build.status.text}, schema apply failed")
		t.BuildProblem(r.Error, teamcity.WithIdentity("atlas-schema-apply"))
	case r.Applied != nil && r.Applied.Error != nil:
		t.AddBuildTag("schema-apply-failed")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, schema apply failed after %d statement(s)", appliedStmts))
		t.BuildProblem(r.Applied.Error.Text, teamcity.WithIdentity("atlas-schema-apply-stmt"))
	case !hasError && appliedStmts > 0:
		t.AddBuildTag("schema-applied")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, schema applied %d statement(s)", appliedStmts))
	default:
		t.AddBuildTag("schema-apply-noop")
		t.BuildStatus("{build.status.text}, no schema changes to apply")
	}
	// Detailed block output for build log.
	const blockName = "atlas schema apply"
	flowID := teamcity.WithFlowID("schema-apply")
	t.BlockOpened(blockName, teamcity.WithDescriptionF(
		"%d statement(s)", appliedStmts), flowID)
	defer t.BlockClosed(blockName, flowID)
	if r.Error != "" {
		t.Message("ERROR", r.Error, flowID)
		return
	}
	if durationMs > 0 {
		t.Message("NORMAL", fmt.Sprintf("Duration: %dms", durationMs), flowID)
	}
	if a := r.Applied; a != nil {
		t.Message("NORMAL", fmt.Sprintf("Applied %d statement(s)", len(a.Applied)), flowID)
		for i, stmt := range a.Applied {
			// Show first few statements in log for visibility.
			if i < 5 {
				stmtPreview := stmt
				if len(stmtPreview) > 100 {
					stmtPreview = stmtPreview[:97] + "..."
				}
				t.Message("NORMAL", fmt.Sprintf("  [%d] %s", i+1, stmtPreview), flowID)
			} else if i == 5 {
				t.Message("NORMAL", fmt.Sprintf("  ... and %d more statement(s)", len(a.Applied)-5), flowID)
				break
			}
		}
		if a.Error != nil {
			t.Message("ERROR", fmt.Sprintf("Error in statement: %s", a.Error.Text), flowID)
		}
	}
}

// planReport reports the results of a schema plan to TeamCity,
// including lint results if present.
func (t *TeamCity) planReport(r *atlasexec.SchemaPlan, tagPrefix string) {
	switch {
	case r == nil:
	case r.Error != "":
		t.AddBuildTag("schema-plan-failed")
		t.BuildStatus("{build.status.text}, schema plan failed")
		t.BuildProblem(r.Error, teamcity.WithIdentity("atlas-schema-plan"))
	case r.File == nil:
		t.AddBuildTag("schema-plan-noop")
		t.BuildStatus("{build.status.text}, no schema changes detected")
	default:
		status := "pending"
		f := r.File
		if f.Status != "" {
			status = strings.ToLower(string(f.Status))
		}
		t.AddBuildTag(fmt.Sprintf("schema-plan-%s", status))
		switch diags, ok := t.lintReport(r.Lint, tagPrefix); {
		case !ok:
			// Only update status if lint passed, otherwise the lintReport
			// will set the status to failed and we don't want to override it.
		case diags > 0:
			t.BuildStatus(fmt.Sprintf("{build.status.text}, schema plan %s (%s, %d lint issue(s))", f.Name, status, diags))
		default:
			t.BuildStatus(fmt.Sprintf("{build.status.text}, schema plan %s (%s)", f.Name, status))
		}
		// Detailed block output for build log.
		const blockName = "atlas schema plan"
		flowID := teamcity.WithFlowID("schema-plan")
		t.BlockOpened(blockName, teamcity.WithDescription(f.Name), flowID)
		defer t.BlockClosed(blockName, flowID)
		t.Message("NORMAL", fmt.Sprintf("Plan: %s", f.Name), flowID)
		if f.Status != "" {
			t.Message("NORMAL", fmt.Sprintf("Status: %s", f.Status), flowID)
		}
		if f.URL != "" {
			t.Message("NORMAL", fmt.Sprintf("URL: %s", f.URL), flowID)
		}
		if f.Link != "" {
			t.Message("NORMAL", fmt.Sprintf("Link: %s", f.Link), flowID)
		}
	}
}

// lintReport reports the results of a lint report to TeamCity and
// returns the number of diagnostics and whether the lint passed without errors.
func (t *TeamCity) lintReport(r *atlasexec.SummaryReport, tagPrefix string) (int, bool) {
	const (
		typeID = "atlas-lint-report"
		name   = "Lint report"
	)
	flowID := teamcity.WithFlowID("lint-report")
	if r == nil || len(r.Files) == 0 {
		t.BuildStatisticValue("atlas.migrate.lint.files", "0")
		t.BuildStatisticValue("atlas.migrate.lint.diagnostics", "0")
		t.BuildStatisticValue("atlas.migrate.lint.errors", "0")
		t.Message("NORMAL", "Lint report completed with no findings", flowID)
		return 0, true
	}
	// Calculate statistics.
	var filesWithIssues int
	var errorNames []string
	files := len(r.Files)
	var errors, diagnostics int
	for _, f := range r.Files {
		if f.Error != "" {
			errors++
			errorNames = append(errorNames, f.Name)
		}
		hasIssues := f.Error != ""
		for _, report := range f.Reports {
			diagnostics += len(report.Diagnostics)
			if len(report.Diagnostics) > 0 {
				hasIssues = true
			}
		}
		if hasIssues {
			filesWithIssues++
		}
	}
	t.BuildStatisticValue("atlas.migrate.lint.files", fmt.Sprintf("%d", files))
	t.BuildStatisticValue("atlas.migrate.lint.diagnostics", fmt.Sprintf("%d", diagnostics))
	t.BuildStatisticValue("atlas.migrate.lint.errors", fmt.Sprintf("%d", errors))
	t.BlockOpened(name, flowID, teamcity.WithDescriptionF(
		"%d file(s), %d issue(s)", filesWithIssues, diagnostics))
	defer t.BlockClosed(name, flowID)
	t.InspectionType(typeID, "Atlas Lint Report", "atlas",
		"Migration lint report", flowID)
	for _, f := range r.Files {
		if f.Error != "" {
			t.Message("ERROR", fmt.Sprintf("File %s: %s", f.Name, f.Error), flowID)
		} else if len(f.Reports) > 0 {
			// Count diagnostics in this file.
			diagCount := 0
			for _, report := range f.Reports {
				diagCount += len(report.Diagnostics)
			}
			t.Message("WARNING", fmt.Sprintf("File %s: %d issue(s)", f.Name, diagCount), flowID)
		}
		for _, report := range f.Reports {
			for _, diag := range report.Diagnostics {
				severity := "WARNING"
				if f.Error != "" {
					severity = "ERROR"
				}
				// Build rich message with link.
				msg := diag.Text
				if diag.Code != "" {
					msg = fmt.Sprintf(`<a href="https://atlasgo.io/lint/analyzers#%s">%s</a> %s`, diag.Code, diag.Code, diag.Text)
				}
				opts := []teamcity.Option{
					teamcity.WithSeverity(severity),
					teamcity.WithMessage(msg),
					flowID,
				}
				// Calculate line number from position
				if diag.Pos > 0 && diag.Pos <= len(f.Text) {
					lines := strings.Split(f.Text[:diag.Pos], "\n")
					opts = append(opts, teamcity.WithLine(max(1, len(lines))))
				}
				t.Inspection(typeID, f.Name, opts...)
			}
		}
	}
	switch {
	case tagPrefix == "": // No status update for non-lint reports, just return the findings.
	case errors > 0:
		t.AddBuildTag(tagPrefix + "-failed")
		errFiles := strings.Join(errorNames, ", ")
		if len(errFiles) > 50 {
			errFiles = errFiles[:47] + "..."
		}
		t.BuildProblem(fmt.Sprintf("Lint found %d error(s) in %d file(s)", errors, files),
			teamcity.WithIdentity("atlas-migrate-lint"))
		t.BuildStatus(fmt.Sprintf("{build.status.text}, lint failed: %s", errFiles))
		return diagnostics, false
	case diagnostics > 0:
		t.AddBuildTag(tagPrefix + "-issues")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, lint found %d issue(s) in %d file(s)", diagnostics, files))
	default:
		t.AddBuildTag(tagPrefix + "-clean")
		t.BuildStatus(fmt.Sprintf("{build.status.text}, lint passed (%d file(s))", files))
	}
	return diagnostics, true
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

// durationMs calculates duration in milliseconds between start and end times.
// Returns 0 if either time is zero.
func durationMs(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

var (
	_ Action   = (*TeamCity)(nil)
	_ Reporter = (*TeamCity)(nil)
)
