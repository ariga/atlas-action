// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package teamcity_test

import (
	"bytes"
	"errors"
	"testing"

	"ariga.io/atlas-action/internal/teamcity"
	"github.com/stretchr/testify/require"
)

func TestServiceMessageWrites(t *testing.T) {
	tests := []struct {
		name string
		act  func(*teamcity.ServiceMessage)
		want string
	}{
		{
			name: "MessageWithOptions",
			act: func(sm *teamcity.ServiceMessage) {
				sm.Message("warning", "build failed",
					teamcity.WithFlowID("flow-1"),
					teamcity.WithErrorDetails("stack trace"),
				)
			},
			want: "##teamcity[message errorDetails='stack trace' flowId='flow-1' status='WARNING' text='build failed']\n",
		},
		{
			name: "BlockOpenedWithDescription",
			act: func(sm *teamcity.ServiceMessage) {
				sm.BlockOpened("lint", teamcity.WithDescription("Lint Results"))
			},
			want: "##teamcity[blockOpened description='Lint Results' name='lint']\n",
		},
		{
			name: "BlockClosed",
			act: func(sm *teamcity.ServiceMessage) {
				sm.BlockClosed("lint")
			},
			want: "##teamcity[blockClosed name='lint']\n",
		},
		{
			name: "ProgressMessage",
			act: func(sm *teamcity.ServiceMessage) {
				sm.ProgressMessage("Compiling")
			},
			want: "##teamcity[progressMessage 'Compiling']\n",
		},
		{
			name: "ProgressStartWithFlow",
			act: func(sm *teamcity.ServiceMessage) {
				sm.ProgressStart("Deploy", teamcity.WithFlowID("flow-1"))
			},
			want: "##teamcity[progressStart 'Deploy' flowId='flow-1']\n",
		},
		{
			name: "ProgressFinish",
			act: func(sm *teamcity.ServiceMessage) {
				sm.ProgressFinish("Deploy")
			},
			want: "##teamcity[progressFinish 'Deploy']\n",
		},
		{
			name: "CompilationStarted",
			act: func(sm *teamcity.ServiceMessage) {
				sm.CompilationStarted("go", teamcity.WithDescription("Compile"))
			},
			want: "##teamcity[compilationStarted compiler='go' description='Compile']\n",
		},
		{
			name: "CompilationFinished",
			act: func(sm *teamcity.ServiceMessage) {
				sm.CompilationFinished("go")
			},
			want: "##teamcity[compilationFinished compiler='go']\n",
		},
		{
			name: "FlowStartedWithParent",
			act: func(sm *teamcity.ServiceMessage) {
				sm.FlowStarted("child", teamcity.WithParentID("parent"))
			},
			want: "##teamcity[flowStarted flowId='child' parentId='parent']\n",
		},
		{
			name: "FlowFinished",
			act: func(sm *teamcity.ServiceMessage) {
				sm.FlowFinished("child")
			},
			want: "##teamcity[flowFinished flowId='child']\n",
		},
		{
			name: "TestSuiteStarted",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestSuiteStarted("suite", teamcity.WithTimestamp("2008-09-03T14:02:34.487"))
			},
			want: "##teamcity[testSuiteStarted name='suite' timestamp='2008-09-03T14:02:34.487']\n",
		},
		{
			name: "TestSuiteFinished",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestSuiteFinished("suite")
			},
			want: "##teamcity[testSuiteFinished name='suite']\n",
		},
		{
			name: "TestStarted",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestStarted("Test1", teamcity.WithCaptureStandardOutput(true))
			},
			want: "##teamcity[testStarted captureStandardOutput='true' name='Test1']\n",
		},
		{
			name: "TestFinished",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestFinished("Test1", teamcity.WithDuration(123))
			},
			want: "##teamcity[testFinished duration='123' name='Test1']\n",
		},
		{
			name: "TestFailedComparison",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestFailed("Test1",
					teamcity.WithType("comparisonFailure"),
					teamcity.WithMessage("failed"),
					teamcity.WithDetails("stack"),
					teamcity.WithExpected("expected"),
					teamcity.WithActual("actual"),
				)
			},
			want: "##teamcity[testFailed actual='actual' details='stack' expected='expected' message='failed' name='Test1' type='comparisonFailure']\n",
		},
		{
			name: "TestIgnored",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestIgnored("Test1", teamcity.WithMessage("skip"))
			},
			want: "##teamcity[testIgnored message='skip' name='Test1']\n",
		},
		{
			name: "TestStdOut",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestStdOut("Test1", "output", teamcity.WithTimestamp("2008-09-03T14:02:34.487"))
			},
			want: "##teamcity[testStdOut name='Test1' out='output' timestamp='2008-09-03T14:02:34.487']\n",
		},
		{
			name: "TestStdErr",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestStdErr("Test1", "error")
			},
			want: "##teamcity[testStdErr name='Test1' out='error']\n",
		},
		{
			name: "TestRetrySupport",
			act: func(sm *teamcity.ServiceMessage) {
				sm.TestRetrySupport(true)
			},
			want: "##teamcity[testRetrySupport enabled='true']\n",
		},
		{
			name: "InspectionType",
			act: func(sm *teamcity.ServiceMessage) {
				sm.InspectionType("atlas-lint", "Atlas Lint", "atlas", "Lint")
			},
			want: "##teamcity[inspectionType category='atlas' description='Lint' id='atlas-lint' name='Atlas Lint']\n",
		},
		{
			name: "InspectionWithOptions",
			act: func(sm *teamcity.ServiceMessage) {
				sm.Inspection("atlas-lint", "schema.hcl",
					teamcity.WithLine(12),
					teamcity.WithSeverity("weak warning"),
					teamcity.WithMessage("fix indentation"),
				)
			},
			want: "##teamcity[inspection SEVERITY='WEAK WARNING' file='schema.hcl' line='12' message='fix indentation' typeId='atlas-lint']\n",
		},
		{
			name: "PublishArtifacts",
			act: func(sm *teamcity.ServiceMessage) {
				sm.PublishArtifacts("artifacts/*.zip")
			},
			want: "##teamcity[publishArtifacts 'artifacts/*.zip']\n",
		},
		{
			name: "PublishNuGetPackage",
			act: func(sm *teamcity.ServiceMessage) {
				sm.PublishNuGetPackage()
			},
			want: "##teamcity[publishNuGetPackage]\n",
		},
		{
			name: "StreamToBuildLog",
			act: func(sm *teamcity.ServiceMessage) {
				sm.StreamToBuildLog("build.log",
					teamcity.WithCharset("utf-8"),
					teamcity.WithWrapFileContentInBlock(true),
				)
			},
			want: "##teamcity[importData charset='utf-8' filePath='build.log' type='streamToBuildLog' wrapFileContentInBlock='true']\n",
		},
		{
			name: "ImportData",
			act: func(sm *teamcity.ServiceMessage) {
				sm.ImportData("junit", teamcity.WithPath("reports/*.xml"), teamcity.WithFlowID("flow-1"))
			},
			want: "##teamcity[importData flowId='flow-1' path='reports/*.xml' type='junit']\n",
		},
		{
			name: "SlackWithConnection",
			act: func(sm *teamcity.ServiceMessage) {
				sm.Slack("#atlas", "build finished", teamcity.WithSlackConnectionID("conn-1"))
			},
			want: "##teamcity[notification connectionId='conn-1' message='build finished' notifier='slack' sendTo='#atlas']\n",
		},
		{
			name: "Email",
			act: func(sm *teamcity.ServiceMessage) {
				sm.Email("dev@atlas", "Subject", "Body")
			},
			want: "##teamcity[notification address='dev@atlas' message='Body' notifier='email' subject='Subject']\n",
		},
		{
			name: "BuildStopWithRequeue",
			act: func(sm *teamcity.ServiceMessage) {
				sm.BuildStop("Maintenance", teamcity.WithReaddToQueue(true))
			},
			want: "##teamcity[buildStop comment='Maintenance' readdToQueue='true']\n",
		},
		{
			name: "SkipQueuedBuildsWithTags",
			act: func(sm *teamcity.ServiceMessage) {
				sm.SkipQueuedBuilds("Maintenance", teamcity.WithTags("hotfix", "urgent"))
			},
			want: "##teamcity[skipQueuedBuilds comment='Maintenance' tags='hotfix,urgent']\n",
		},
		{
			name: "AddBuildTag",
			act: func(sm *teamcity.ServiceMessage) {
				sm.AddBuildTag("release")
			},
			want: "##teamcity[addBuildTag 'release']\n",
		},
		{
			name: "RemoveBuildTag",
			act: func(sm *teamcity.ServiceMessage) {
				sm.RemoveBuildTag("release")
			},
			want: "##teamcity[removeBuildTag 'release']\n",
		},
		{
			name: "UndoPersonalPatch",
			act: func(sm *teamcity.ServiceMessage) {
				sm.UndoPersonalPatch()
			},
			want: "##teamcity[undoPersonalPatch]\n",
		},
		{
			name: "SetParameter",
			act: func(sm *teamcity.ServiceMessage) {
				sm.SetParameter("env.BRANCH", "main")
			},
			want: "##teamcity[setParameter name='env.BRANCH' value='main']\n",
		},
		{
			name: "BuildProblemWithIdentity",
			act: func(sm *teamcity.ServiceMessage) {
				sm.BuildProblem("Build failed", teamcity.WithIdentity("build-123"))
			},
			want: "##teamcity[buildProblem description='Build failed' identity='build-123']\n",
		},
		{
			name: "BuildStatus",
			act: func(sm *teamcity.ServiceMessage) {
				sm.BuildStatus("SUCCESS")
			},
			want: "##teamcity[buildStatus text='SUCCESS']\n",
		},
		{
			name: "BuildStatusForce",
			act: func(sm *teamcity.ServiceMessage) {
				sm.BuildStatus("Still green", teamcity.WithStatus("success"))
			},
			want: "##teamcity[buildStatus status='SUCCESS' text='Still green']\n",
		},
		{
			name: "BuildNumber",
			act: func(sm *teamcity.ServiceMessage) {
				sm.BuildNumber("1.2.3_{build.number}")
			},
			want: "##teamcity[buildNumber '1.2.3_{build.number}']\n",
		},
		{
			name: "BuildStatisticValue",
			act: func(sm *teamcity.ServiceMessage) {
				sm.BuildStatisticValue("coverage", "80.5")
			},
			want: "##teamcity[buildStatisticValue key='coverage' value='80.5']\n",
		},
		{
			name: "EnableServiceMessages",
			act: func(sm *teamcity.ServiceMessage) {
				sm.EnableServiceMessages(teamcity.WithFlowID("flow-1"))
			},
			want: "##teamcity[enableServiceMessages flowId='flow-1']\n",
		},
		{
			name: "DisableServiceMessages",
			act: func(sm *teamcity.ServiceMessage) {
				sm.DisableServiceMessages()
			},
			want: "##teamcity[disableServiceMessages]\n",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			sm := teamcity.NewServiceMessage(&buf)
			tt.act(sm)
			require.Equal(t, tt.want, buf.String())
		})
	}
}

func TestServiceMessageWriteError(t *testing.T) {
	sm := teamcity.NewServiceMessage(errWriter{})
	require.PanicsWithError(t, "failed to write message: boom", func() {
		sm.BuildStatus("SUCCESS")
	})
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("boom")
}
