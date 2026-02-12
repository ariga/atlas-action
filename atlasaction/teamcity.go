// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf16"

	"ariga.io/atlas/atlasexec"
	"github.com/magiconair/properties"
)

type (
	TeamCity struct {
		w      io.Writer
		getenv func(string) string
	}
)

// NewTeamCity creates a new TeamCity action.
func NewTeamCity(getenv func(string) string, w io.Writer) *TeamCity {
	return &TeamCity{getenv: getenv, w: w}
}

// Infof implements [Action].
func (t *TeamCity) Infof(msg string, a ...any) {
	t.message("message", "text", fmt.Sprintf(msg, a...), "status", "NORMAL")
}

// Warningf implements [Action].
func (t *TeamCity) Warningf(msg string, a ...any) {
	t.message("message", "text", fmt.Sprintf(msg, a...), "status", "WARNING")
}

// Errorf implements [Action].
func (t *TeamCity) Errorf(msg string, a ...any) {
	t.message("message", "text", fmt.Sprintf(msg, a...), "status", "ERROR")
}

// Fatalf implements [Action].
func (t *TeamCity) Fatalf(msg string, a ...any) {
	t.message("message", "text", fmt.Sprintf(msg, a...), "status", "FAILURE")
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
	if u, err := url.Parse(tc.RepoURL); err == nil && u.Host != "" {
		host := strings.ToLower(u.Hostname())
		switch {
		case host == "github.com" || strings.HasSuffix(host, ".github.com"):
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
		case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com") || strings.Contains(host, "gitlab"):
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
		case host == "bitbucket.org" || strings.HasSuffix(host, ".bitbucket.org"):
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
	t.message("setParameter", "name", name, "value", value)
	// Also set as environment variable, to allow usage in subsequent build steps.
	env := toOutputVarName(t.getenv("ATLAS_ACTION_COMMAND"), name)
	t.message("setParameter", "name", fmt.Sprintf("env.%s", env), "value", value)
}

func (t *TeamCity) buildProperties() (*properties.Properties, error) {
	path := strings.TrimSpace(t.getenv("TEAMCITY_BUILD_PROPERTIES_FILE"))
	if path == "" {
		return nil, fmt.Errorf("TEAMCITY_BUILD_PROPERTIES_FILE is not set")
	}
	return properties.LoadFile(path, properties.UTF8)
}

// message sends a TeamCity service message.
func (t *TeamCity) message(typ string, attrs ...string) {
	fmt.Fprint(t.w, "##teamcity[")
	fmt.Fprint(t.w, typ)
	switch l := len(attrs); {
	case l == 1:
		fmt.Fprint(t.w, " '")
		fmt.Fprint(t.w, t.escapeString(attrs[0]))
		fmt.Fprint(t.w, "'")
	case l > 1:
		if l%2 != 0 {
			// If odd number of attributes, log a failure message
			fmt.Fprint(t.w, "]\n")
			t.Fatalf("message() called with odd number of attributes (%d): %v", l, attrs)
			return
		}
		for i := 0; i < len(attrs); i += 2 {
			fmt.Fprint(t.w, " ")
			fmt.Fprint(t.w, t.escapeString(attrs[i]))
			fmt.Fprint(t.w, "='")
			fmt.Fprint(t.w, t.escapeString(attrs[i+1]))
			fmt.Fprint(t.w, "'")
		}
	}
	fmt.Fprint(t.w, "]\n")
}

// escapeString escapes a string according to TeamCity service message rules.
// https://www.jetbrains.com/help/teamcity/service-messages.html#Escaped+Values
func (t *TeamCity) escapeString(val string) string {
	b := make([]byte, 0, len(val))
	for _, r := range val {
		switch {
		case r == '\n':
			b = append(b, '|', 'n')
		case r == '\r':
			b = append(b, '|', 'r')
		case r == '\u0085':
			b = append(b, '|', 'x')
		case r == '\u2028':
			b = append(b, '|', 'l')
		case r == '\u2029':
			b = append(b, '|', 'p')
		case r == '|', r == '[', r == ']', r == '\'':
			b = append(b, '|', byte(r))
		case r <= 127: // unicode.MaxASCII
			b = append(b, byte(r))
		case r <= 0xFFFF:
			// Characters in the Basic Multilingual Plane (BMP)
			b = fmt.Appendf(b, "|0x%04X", r)
		default:
			// Non-BMP characters (> 0xFFFF) need to be encoded as UTF-16 surrogate pairs
			// TeamCity expects two |0xXXXX sequences for these characters
			pair := utf16.Encode([]rune{r})
			for _, code := range pair {
				b = fmt.Appendf(b, "|0x%04X", code)
			}
		}
	}
	return string(b)
}

var _ Action = (*TeamCity)(nil)
