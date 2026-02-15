// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas/atlasexec"
	"ariga.io/atlas/sql/schema"
	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

func TestTeamCity_SCMDetection(t *testing.T) {
	tests := []struct {
		name        string
		repoURL     string
		expectedSCM atlasexec.SCMType
	}{
		{
			name:        "github.com HTTPS",
			repoURL:     "https://github.com/ariga/atlas-action.git",
			expectedSCM: atlasexec.SCMTypeGithub,
		},
		{
			name:        "GitHub Enterprise with .github.com",
			repoURL:     "https://gh.github.com/ariga/atlas-action.git",
			expectedSCM: atlasexec.SCMTypeGithub,
		},
		{
			name:        "gitlab.com HTTPS",
			repoURL:     "https://gitlab.com/ariga/atlas-action.git",
			expectedSCM: atlasexec.SCMTypeGitlab,
		},
		{
			name:        "GitLab self-hosted with .gitlab.com",
			repoURL:     "https://eu.gitlab.com/ariga/atlas-action.git",
			expectedSCM: atlasexec.SCMTypeGitlab,
		},
		{
			name:        "bitbucket.org HTTPS",
			repoURL:     "https://bitbucket.org/ariga/atlas-action.git",
			expectedSCM: atlasexec.SCMTypeBitbucket,
		},
		{
			name:        "false positive - path contains github",
			repoURL:     "https://example.com/path/github/repo.git",
			expectedSCM: atlasexec.SCMType(""),
		},
		{
			name:        "false positive - hostname like github.com.example.com",
			repoURL:     "https://github.com.example.com/repo.git",
			expectedSCM: atlasexec.SCMType(""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary properties file
			tmpDir := t.TempDir()
			propsFile := filepath.Join(tmpDir, "build.properties")
			content := `teamcity.projectName=test-project
build.vcs.number=abc123
teamcity.build.branch=main
vcsroot.url=` + tt.repoURL
			err := os.WriteFile(propsFile, []byte(content), 0600)
			require.NoError(t, err)

			getenv := func(key string) string {
				if key == "TEAMCITY_BUILD_PROPERTIES_FILE" {
					return propsFile
				}
				return ""
			}

			var buf bytes.Buffer
			tc := atlasaction.NewTeamCity(getenv, &buf)
			ctx, err := tc.GetTriggerContext(context.Background())
			require.NoError(t, err)
			require.Equal(t, tt.expectedSCM, ctx.SCMType)
		})
	}
}

func TestTeamCity_SchemaLint(t *testing.T) {
	var buf bytes.Buffer
	tc := atlasaction.NewTeamCity(func(string) string { return "" }, &buf)
	pos := &schema.Pos{Filename: "schema.hcl"}
	pos.Start.Line = 12
	report := &atlasaction.SchemaLintReport{
		URL: []string{"file://schema.hcl"},
		SchemaLintReport: &atlasexec.SchemaLintReport{
			Steps: []atlasexec.Report{
				{
					Text:  "destructive change",
					Desc:  "Dropping column users.email",
					Error: true,
					Diagnostics: []atlasexec.Diagnostic{
						{
							Text: "Non-virtual column was dropped",
							Code: "DS103",
							Pos:  pos,
						},
					},
				},
				{
					Text: "naming",
					Desc: "Table naming convention",
					Diagnostics: []atlasexec.Diagnostic{
						{
							Text: "Table name should be snake_case",
						},
					},
				},
			},
		},
	}
	tc.SchemaLint(context.Background(), report)
	require.Equal(t, `##teamcity[blockOpened description='file://schema.hcl' flowId='schema-lint' name='atlas schema lint']
##teamcity[inspectionType category='atlas' description='Schema lint checks' flowId='schema-lint' id='atlas-schema-lint' name='Atlas Schema Lint']
##teamcity[message flowId='schema-lint' status='ERROR' text='destructive change Dropping column users.email']
##teamcity[inspection SEVERITY='ERROR' file='schema.hcl' flowId='schema-lint' line='12' message='<a href="https://atlasgo.io/lint/analyzers#DS103">Non-virtual column was dropped</a>' typeId='atlas-schema-lint']
##teamcity[message flowId='schema-lint' status='WARNING' text='naming Table naming convention']
##teamcity[blockClosed flowId='schema-lint' name='atlas schema lint']
`, buf.String())
}

func TestTeamCity(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "teamcity"),
		Setup: func(e *testscript.Env) error {
			// Create build.properties file for TeamCity
			propsFile := filepath.Join(e.WorkDir, "build.properties")
			propsContent := `teamcity.projectName=test-project
build.vcs.number=abc123
teamcity.build.branch=main
vcsroot.url=https://github.com/ariga/atlas-action.git`
			if err := os.WriteFile(propsFile, []byte(propsContent), 0600); err != nil {
				return err
			}
			e.Setenv("MOCK_ATLAS", filepath.Join(wd, "mock-atlas.sh"))
			e.Setenv("TEAMCITY_VERSION", "2023.05")
			e.Setenv("TEAMCITY_BUILD_PROPERTIES_FILE", propsFile)
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"output": func(ts *testscript.TestScript, neg bool, args []string) {
				// For TeamCity, output is written to stdout as service messages.
				// This command is a no-op placeholder for consistency.
				if neg {
					return
				}
			},
		},
	})
}
