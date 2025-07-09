// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/fatih/color"
)

type (
	Azure struct {
		w      io.Writer
		getenv func(string) string
	}
	azureEndpointAuthorization struct {
		Scheme     string            `json:"scheme"`
		Parameters map[string]string `json:"parameters"`
	}
)

var _ Action = (*Azure)(nil)

// NewAzure returns a new Action for Azure DevOps.
func NewAzure(getenv func(string) string, w io.Writer) *Azure {
	return &Azure{getenv: getenv, w: w}
}

// GetType implements the Action interface.
func (*Azure) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeAzureDevOps
}

// Getenv implements Action.
func (a *Azure) Getenv(key string) string {
	return a.getenv(key)
}

// GetInput implements the Action interface.
func (a *Azure) GetInput(name string) string {
	return strings.TrimSpace(a.getenv(fmt.Sprintf("INPUT_%s",
		toEnvName(name))))
}

// SetOutput implements Action.
//
// It writes a task.setvariable logging command to set an output variable in Azure DevOps.
// https://learn.microsoft.com/en-us/azure/devops/pipelines/process/set-variables-scripts?view=azure-devops&tabs=bash#set-variable-properties
func (a *Azure) SetOutput(key, value string) {
	a.command("task.setvariable", value, map[string]string{
		"variable": key,
		"isOutput": "true",
		"isSecret": "false",
	})
}

// GetTriggerContext implements Action.
//
// It build the TriggerContext from the Azure DevOps environment variables.
// For full list of available variables, see:
// https://learn.microsoft.com/en-us/azure/devops/pipelines/build/variables
func (a *Azure) GetTriggerContext(context.Context) (_ *TriggerContext, err error) {
	tc := &TriggerContext{
		Act:     a,
		RepoURL: a.getVar("Build.Repository.Uri"),
		Repo:    a.getVar("Build.Repository.Name"),
		Branch:  a.getVar("Build.SourceBranch"),
		Commit:  a.getVar("Build.SourceVersion"),
		Actor:   &Actor{Name: a.getVar("Build.SourceVersionAuthor")},
	}
	if c := a.getVar("System.PullRequest.SourceCommitId"); c != "" {
		pr := &PullRequest{Commit: c}
		pr.Number, err = strconv.Atoi(a.getVar("System.PullRequest.PullRequestNumber"))
		if err != nil {
			return nil, fmt.Errorf("failed to parse System.PullRequest.PullRequestNumber: %w", err)
		}
		tc.PullRequest = pr
		tc.Commit = c
		tc.Branch = a.getVar("System.PullRequest.SourceBranch")
	}
	switch p := a.getVar("Build.Repository.Provider"); p {
	case "GitHub":
		tc.SCMType = atlasexec.SCMTypeGithub
		if pr := tc.PullRequest; pr != nil {
			pr.URL, err = url.JoinPath(tc.RepoURL, "pull", strconv.Itoa(pr.Number))
			if err != nil {
				return nil, fmt.Errorf("failed to construct pull request URL: %w", err)
			}
		}
		tc.SCMClient = func() (SCMClient, error) {
			var token string
			if c := a.GetInput("githubConnection"); c != "" {
				token, err = a.getGHToken(c)
				if err != nil {
					return nil, fmt.Errorf("failed to get GitHub token for connection %s: %w", c, err)
				}
				if token == "" {
					a.Warningf("the githubConnection input is set, but no token was found")
				}
			} else {
				a.Warningf("the githubConnection input is not set, the action may not have all the permissions")
			}
			return NewGitHubClient(tc.Repo, a.getenv("GITHUB_API_URL"), token)
		}
	case "Bitbucket", "TfsGit", "TfsVersionControl", "Git", "Svn":
		a.Warningf("Unsupported repository provider: %q", p)
	default:
		return nil, fmt.Errorf("unknown BUILD_REPOSITORY_PROVIDER %q", p)
	}
	return tc, nil
}

func (a *Azure) getGHToken(endpoint string) (string, error) {
	switch az, err := a.getEndpointAuthorization(endpoint); {
	case err != nil:
		return "", err
	case az == nil:
		return "", nil
	case az.Scheme == "PersonalAccessToken":
		t, ok := az.Parameters["accessToken"]
		if !ok {
			return "", fmt.Errorf("missing accessToken in ENDPOINT_AUTH_%s", endpoint)
		}
		return t, nil
	case az.Scheme == "OAuth", az.Scheme == "Token":
		t, ok := az.Parameters["AccessToken"]
		if !ok {
			return "", fmt.Errorf("missing AccessToken in ENDPOINT_AUTH_%s", endpoint)
		}
		return t, nil
	case az.Scheme != "":
		return "", fmt.Errorf("unsupported scheme %q", az.Scheme)
	default:
		return "", errors.New("no scheme found")
	}
}

func (a *Azure) getEndpointAuthorization(id string) (*azureEndpointAuthorization, error) {
	v := a.getenv("ENDPOINT_AUTH_" + id)
	if v == "" {
		return nil, fmt.Errorf("ENDPOINT_AUTH_%s is not set", id)
	}
	var auth azureEndpointAuthorization
	if err := json.Unmarshal([]byte(v), &auth); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Endpoint.Auth.%s: %w", id, err)
	}
	return &auth, nil
}

func (a *Azure) getVar(name string) string {
	return a.getenv(strings.ToUpper(strings.ReplaceAll(name, ".", "_")))
}

// Infof implements the Logger interface.
func (l *Azure) Infof(msg string, args ...any) {
	fmt.Fprintln(l.w, color.CyanString(msg, args...))
}

// Warningf implements the Logger interface.
func (l *Azure) Warningf(msg string, args ...any) {
	l.command("task.issue", fmt.Sprintf(msg, args...), map[string]string{
		"type":   "warning",
		"source": "TaskInternal",
	})
}

// Errorf implements the Logger interface.
func (l *Azure) Errorf(msg string, args ...any) {
	l.command("task.issue", fmt.Sprintf(msg, args...), map[string]string{
		"type":   "error",
		"source": "TaskInternal",
	})
}

// Fatalf implements the Logger interface.
func (l *Azure) Fatalf(msg string, args ...any) {
	l.Errorf(msg, args...)
	os.Exit(1)
}

// command formats and writes a command to the Azure DevOps log.
// The command is formatted as per the Azure DevOps logging commands:
// https://learn.microsoft.com/en-us/azure/devops/pipelines/scripts/logging-commands?view=azure-devops&tabs=bash
func (a *Azure) command(name, message string, props map[string]string) {
	var (
		r0     = strings.NewReplacer("%", "%AZP25", "\r", "%0D", "\n", "%0A", "]", "%5D", ";", "%3B")
		escape = func(s string) string { return r0.Replace(s) }
	)
	var (
		r1         = strings.NewReplacer("%", "%AZP25", "\r", "%0D", "\n", "%0A")
		escapeData = func(s string) string { return r1.Replace(s) }
	)
	fmt.Fprintf(a.w, "##vso[%s", name)
	if len(props) > 0 {
		fmt.Fprint(a.w, " ")
		for _, k := range slices.Sorted(maps.Keys(props)) {
			fmt.Fprintf(a.w, "%s=%s;", k, escape(props[k]))
		}
	}
	fmt.Fprintf(a.w, "]%s\n", escapeData(message))
}
