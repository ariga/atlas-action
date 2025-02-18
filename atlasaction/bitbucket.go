// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ariga.io/atlas-action/internal/bitbucket"
	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/fatih/color"
	"golang.org/x/oauth2"
)

type bbPipe struct {
	*coloredLogger
	getenv func(string) string
}

// NewBitBucketPipe returns a new Action for BitBucket.
func NewBitBucketPipe(getenv func(string) string, w io.Writer) *bbPipe {
	// Disable color output for testing,
	// but enable it for non-testing environments.
	color.NoColor = testing.Testing()
	return &bbPipe{getenv: getenv, coloredLogger: &coloredLogger{w: w}}
}

// GetType implements Action.
func (*bbPipe) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeBitbucket
}

// Getenv implements Action.
func (a *bbPipe) Getenv(key string) string {
	return a.getenv(key)
}

// GetTriggerContext implements Action.
func (a *bbPipe) GetTriggerContext(context.Context) (*TriggerContext, error) {
	tc := &TriggerContext{
		Act:     a,
		Branch:  a.getenv("BITBUCKET_BRANCH"),
		Commit:  a.getenv("BITBUCKET_COMMIT"),
		Repo:    a.getenv("BITBUCKET_REPO_FULL_NAME"),
		RepoURL: a.getenv("BITBUCKET_GIT_HTTP_ORIGIN"),
		SCM: SCM{
			Type:   atlasexec.SCMTypeBitbucket,
			APIURL: "https://api.bitbucket.org/2.0",
		},
	}
	if pr := a.getenv("BITBUCKET_PR_ID"); pr != "" {
		var err error
		tc.PullRequest = &PullRequest{
			Commit: a.getenv("BITBUCKET_COMMIT"),
		}
		tc.PullRequest.Number, err = strconv.Atoi(pr)
		if err != nil {
			return nil, err
		}
		// <repo-url>/pull-requests/<pr-id>
		tc.PullRequest.URL, err = url.JoinPath(tc.RepoURL, "pull-requests", pr)
		if err != nil {
			return nil, err
		}
	}
	return tc, nil
}

// GetInput implements the Action interface.
func (a *bbPipe) GetInput(name string) string {
	return strings.TrimSpace(a.getenv(toInputVarName(name)))
}

// SetOutput implements Action.
func (a *bbPipe) SetOutput(name, value string) {
	// Because Bitbucket Pipes does not support output variables,
	// we write the output to a file.
	// So the next step can read the outputs using the source command.
	// e.g:
	// ```shell
	// source .atlas-action/outputs.sh
	// ```
	dir := filepath.Join(a.getenv("BITBUCKET_CLONE_DIR"), ".atlas-action")
	if out := a.getenv("ATLAS_OUTPUT_DIR"); out != "" {
		// The user can set the output directory using
		// the ATLAS_OUTPUT_DIR environment variable.
		// This is useful when the user wants to share the output
		// with steps run outside the pipe.
		dir = out
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		a.Errorf("failed to create output directory %s: %v", dir, err)
		return
	}
	outputs := filepath.Join(dir, "outputs.sh")
	err := fprintln(outputs,
		"export", toOutputVar(a.getenv("ATLAS_ACTION_COMMAND"), name, value))
	if err != nil {
		a.Errorf("failed to write output to file %s: %v", outputs, err)
	}
}

// MigrateApply implements Reporter.
func (a *bbPipe) MigrateApply(context.Context, *atlasexec.MigrateApply) {
}

// MigrateLint implements Reporter.
func (a *bbPipe) MigrateLint(ctx context.Context, r *atlasexec.SummaryReport) {
	c, err := a.reportClient()
	if err != nil {
		a.Errorf("failed to create bitbucket client: %v", err)
		return
	}
	commitID := a.getenv("BITBUCKET_COMMIT")
	cr, err := LintReport(commitID, r)
	if err != nil {
		a.Errorf("failed to generate commit report: %v", err)
		return
	}
	if _, err = c.CreateReport(ctx, commitID, cr); err != nil {
		a.Errorf("failed to create commit report: %v", err)
		return
	}
	if issues := filterIssues(r.Steps); len(issues) > 0 {
		stepSummary := func(s *atlasexec.StepReport) string {
			if s.Text == "" {
				return s.Name
			}
			return fmt.Sprintf("%s: %s", s.Name, s.Text)
		}
		annos := make([]bitbucket.ReportAnnotation, 0, len(issues))
		for _, s := range issues {
			severity := bitbucket.SeverityMedium
			if stepIsError(s) {
				severity = bitbucket.SeverityHigh
			}
			if s.Result == nil {
				anno := bitbucket.ReportAnnotation{
					Result:   bitbucket.ResultFailed,
					Summary:  stepSummary(s),
					Details:  s.Error,
					Severity: severity,
				}
				anno.ExternalID, err = hash(cr.ExternalID, s.Name, s.Text)
				if err != nil {
					a.Errorf("failed to generate external ID: %v", err)
					return
				}
				annos = append(annos, anno)
			} else {
				for _, rr := range s.Result.Reports {
					for _, d := range rr.Diagnostics {
						anno := bitbucket.ReportAnnotation{
							Result:   bitbucket.ResultFailed,
							Summary:  stepSummary(s),
							Details:  fmt.Sprintf("%s: %s", rr.Text, d.Text),
							Severity: severity,
							Path:     "", // TODO: add path
							Line:     0,  // TODO: add line
						}
						switch {
						case d.Code != "":
							anno.Details += fmt.Sprintf(" (%s)", d.Code)
							anno.AnnotationType = bitbucket.AnnotationTypeBug
							anno.Link = fmt.Sprintf("https://atlasgo.io/lint/analyzers#%s", d.Code)
						case len(d.SuggestedFixes) != 0:
							anno.AnnotationType = bitbucket.AnnotationTypeCodeSmell
							// TODO: Add suggested fixes.
						default:
							anno.AnnotationType = bitbucket.AnnotationTypeVulnerability
						}
						anno.ExternalID, err = hash(cr.ExternalID, s.Name, s.Text, rr.Text, d.Text)
						if err != nil {
							a.Errorf("failed to generate external ID: %v", err)
							return
						}
						annos = append(annos, anno)
					}
				}
			}
		}
		_, err = c.CreateReportAnnotations(ctx, commitID, cr.ExternalID, annos)
		if err != nil {
			a.Errorf("failed to create commit report annotations: %v", err)
			return
		}
	}
}

// SchemaApply implements Reporter.
func (a *bbPipe) SchemaApply(context.Context, *atlasexec.SchemaApply) {
}

// SchemaPlan implements Reporter.
func (a *bbPipe) SchemaPlan(ctx context.Context, r *atlasexec.SchemaPlan) {
	if l := r.Lint; l != nil {
		a.MigrateLint(ctx, l)
	}
}

// reportClient returns a new Bitbucket client,
// This client only works with the Reports-API.
func (a *bbPipe) reportClient() (*bitbucket.Client, error) {
	return bitbucket.NewClient(
		a.getenv("BITBUCKET_WORKSPACE"),
		a.getenv("BITBUCKET_REPO_SLUG"),
		// Proxy the request through the Docker host.
		// It allows the pipe submit the report without extra authentication.
		//
		// https://support.atlassian.com/bitbucket-cloud/docs/code-insights/#Authentication
		bitbucket.WithProxy(func() (u *url.URL, err error) {
			u = &url.URL{}
			if h := a.getenv("DOCKER_HOST"); h != "" {
				if u, err = url.Parse(h); err != nil {
					return nil, err
				}
			}
			u.Scheme = "http"
			u.Host = fmt.Sprintf("%s:29418", u.Hostname())
			return u, nil
		}),
	)
}

// We use our docker image name as the reporter.
// This is used to identify the source of the report.
const bitbucketReporter = "arigaio/atlas-action"

// LintReport generates a commit report for the given commit ID
func LintReport(commit string, r *atlasexec.SummaryReport) (*bitbucket.CommitReport, error) {
	// We need ensure the report is unique on Bitbucket.
	// So we hash the commit ID and the current schema.
	// This way, we can identify the report for a specific commit, and schema state.
	externalID, err := hash(commit, r.Schema.Current)
	if err != nil {
		return nil, fmt.Errorf("bitbucket: failed to generate external ID: %w", err)
	}
	cr := &bitbucket.CommitReport{
		ExternalID: externalID,
		Reporter:   bitbucketReporter,
		ReportType: bitbucket.ReportTypeSecurity,
		Title:      "Atlas Lint",
		Link:       r.URL,
		LogoURL:    "https://atlasgo.io/uploads/websiteicon.svg",
	}
	if issues := len(filterIssues(r.Steps)); issues > 0 {
		cr.Details = fmt.Sprintf("Found %d issues.", issues)
		cr.Result = bitbucket.ResultFailed
		steps := len(r.Steps)
		cr.AddPercentage("Health Score", float64(steps-issues)/float64(steps)*100)
	} else {
		cr.Details = "No issues found."
		cr.Result = bitbucket.ResultPassed
		cr.AddPercentage("Health Score", 100)
	}
	cr.AddNumber("Diagnostics", int64(r.DiagnosticsCount()))
	cr.AddNumber("Files", int64(len(r.Files)))
	if d := r.Env.Dir; d != "" {
		cr.AddText("Working Directory", d)
	}
	if r.URL != "" {
		u, err := url.Parse(r.URL)
		if err != nil {
			return nil, fmt.Errorf("bitbucket: failed to parse URL: %w", err)
		}
		u.Fragment = "erd"
		cr.AddLink("ERD", "View Visualization", u)
	}
	return cr, nil
}

type bbClient struct {
	*bitbucket.Client
}

// BitbucketClient returns a new Bitbucket client that implements SCMClient.
func BitbucketClient(workspace, repoSlug, token string) (*bbClient, error) {
	c, err := bitbucket.NewClient(
		workspace, repoSlug,
		bitbucket.WithToken(&oauth2.Token{AccessToken: token}),
	)
	if err != nil {
		return nil, err
	}
	return &bbClient{Client: c}, nil
}

// CommentLint implements SCMClient.
func (c *bbClient) CommentLint(ctx context.Context, tc *TriggerContext, r *atlasexec.SummaryReport) error {
	comment, err := RenderTemplate("migrate-lint/md", r)
	if err != nil {
		return err
	}
	return c.upsertComment(ctx, tc.PullRequest.Number, tc.Act.GetInput("dir-name"), comment)
}

// CommentPlan implements SCMClient.
func (c *bbClient) CommentPlan(ctx context.Context, tc *TriggerContext, p *atlasexec.SchemaPlan) error {
	comment, err := RenderTemplate("schema-plan/md", p)
	if err != nil {
		return err
	}
	return c.upsertComment(ctx, tc.PullRequest.Number, p.File.Name, comment)
}

func (c *bbClient) upsertComment(ctx context.Context, prID int, id, comment string) error {
	comments, err := c.PullRequestComments(ctx, prID)
	if err != nil {
		return err
	}
	marker := commentMarker(id)
	comment += "\n\n" + marker
	if found := slices.IndexFunc(comments, func(c bitbucket.PullRequestComment) bool {
		return strings.Contains(c.Content.Raw, marker)
	}); found != -1 {
		_, err = c.PullRequestUpdateComment(ctx, prID, comments[found].ID, comment)
	} else {
		_, err = c.PullRequestCreateComment(ctx, prID, comment)
	}
	return err
}

func (c *bbClient) IsCoAuthored(context.Context, *TriggerContext) (bool, error) {
	// Not implemented.
	return false, nil
}

// hash returns the SHA-256 hash of the parts.
// The hash is encoded using base64.RawURLEncoding.
func hash(parts ...string) (string, error) {
	h := sha256.New()
	for _, p := range parts {
		if _, err := h.Write([]byte(p)); err != nil {
			return "", err
		}
	}
	return base64.URLEncoding.EncodeToString(h.Sum(nil)), nil
}

var _ Action = (*bbPipe)(nil)
var _ Reporter = (*bbPipe)(nil)
var _ SCMClient = (*bbClient)(nil)
