// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package teamcity

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// DefaultServiceMessage is a default instance of ServiceMessage that writes to standard output.
var DefaultServiceMessage = NewServiceMessage(os.Stdout)

// ServiceMessage provides methods to write TeamCity service messages to the output.
type ServiceMessage struct {
	w io.Writer
}

// NewServiceMessage creates a new ServiceMessage with the given writer.
func NewServiceMessage(w io.Writer) *ServiceMessage {
	return &ServiceMessage{w: w}
}

// WithFlowID returns an Option to set the flowId attribute of the message,
// which specifies the flow to which the message belongs.
// Messages with the same flowId will be grouped together in the build log.
// https://www.jetbrains.com/help/teamcity/service-messages.html#Message+FlowId
func WithFlowID(flowID string) Option {
	return func(attrs MessageAttributes) {
		attrs["flowId"] = flowID
	}
}

// WithErrorDetails returns an Option to set the errorDetails attribute of the message.
func WithErrorDetails(details string) Option {
	return func(attrs MessageAttributes) {
		attrs["errorDetails"] = details
	}
}

// WithTimestamp returns an Option to set the timestamp attribute.
func WithTimestamp(timestamp string) Option {
	return func(attrs MessageAttributes) {
		attrs["timestamp"] = timestamp
	}
}

// WithDuration returns an Option to set the duration attribute in milliseconds.
func WithDuration(duration int) Option {
	return func(attrs MessageAttributes) {
		attrs["duration"] = fmt.Sprintf("%d", duration)
	}
}

// WithCaptureStandardOutput returns an Option to set the captureStandardOutput attribute.
func WithCaptureStandardOutput(capture bool) Option {
	return func(attrs MessageAttributes) {
		attrs["captureStandardOutput"] = boolStr(capture)
	}
}

// WithDetails returns an Option to set the details attribute of the message.
func WithDetails(details string) Option {
	return func(attrs MessageAttributes) {
		attrs["details"] = details
	}
}

// WithExpected returns an Option to set the expected attribute of the message.
func WithExpected(expected string) Option {
	return func(attrs MessageAttributes) {
		attrs["expected"] = expected
	}
}

// WithActual returns an Option to set the actual attribute of the message.
func WithActual(actual string) Option {
	return func(attrs MessageAttributes) {
		attrs["actual"] = actual
	}
}

// WithType returns an Option to set the type attribute of the message.
func WithType(value string) Option {
	return func(attrs MessageAttributes) {
		attrs["type"] = value
	}
}

// WithOut returns an Option to set the out attribute of the message.
func WithOut(out string) Option {
	return func(attrs MessageAttributes) {
		attrs["out"] = out
	}
}

// WithEnabled returns an Option to set the enabled attribute of the message.
func WithEnabled(enabled bool) Option {
	return func(attrs MessageAttributes) {
		attrs["enabled"] = boolStr(enabled)
	}
}

// WithStatus returns an Option to set the status attribute of the message.
func WithStatus(status string) Option {
	return func(attrs MessageAttributes) {
		attrs["status"] = strings.ToUpper(status)
	}
}

// Message reports message to the build log with the given status and text.
// The status can be one of NORMAL, WARNING, ERROR or FAILURE.
func (t *ServiceMessage) Message(status, text string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "message",
		Attrs: MessageAttributes{
			"status": strings.ToUpper(status),
			"text":   text,
		}.WithOptions(opts),
	})
}

// WithDescriptionF returns an Option to set the description attribute of the message,
// formatting the description using fmt.Sprintf with the provided format string and arguments.
func WithDescriptionF(format string, a ...any) Option {
	return WithDescription(fmt.Sprintf(format, a...))
}

// WithDescription returns an Option to set the description attribute of the message,
// which specifies the description of the block to be opened.
func WithDescription(description string) Option {
	return func(attrs MessageAttributes) {
		attrs["description"] = description
	}
}

// BlockOpened writes a TeamCity service message to open a block with the given name.
func (t *ServiceMessage) BlockOpened(name string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "blockOpened",
		Attrs: MessageAttributes{
			"name": name,
		}.WithOptions(opts),
	})
}

// BlockClosed writes a TeamCity service message to close a block with the given name.
func (t *ServiceMessage) BlockClosed(name string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "blockClosed",
		Attrs: MessageAttributes{
			"name": name,
		}.WithOptions(opts),
	})
}

// ProgressMessage writes a TeamCity progress message that is shown until replaced.
func (t *ServiceMessage) ProgressMessage(message string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "progressMessage",
		Attrs: MessageAttributes{
			"": message,
		}.WithOptions(opts),
	})
}

// ProgressStart writes a TeamCity progressStart message for the given block.
func (t *ServiceMessage) ProgressStart(message string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "progressStart",
		Attrs: MessageAttributes{
			"": message,
		}.WithOptions(opts),
	})
}

// ProgressFinish writes a TeamCity progressFinish message for the given block.
func (t *ServiceMessage) ProgressFinish(message string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "progressFinish",
		Attrs: MessageAttributes{
			"": message,
		}.WithOptions(opts),
	})
}

// CompilationStarted writes a TeamCity service message to report
// the start of a compilation with the given compiler name.
func (t *ServiceMessage) CompilationStarted(compiler string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "compilationStarted",
		Attrs: MessageAttributes{
			"compiler": compiler,
		}.WithOptions(opts),
	})
}

// CompilationFinished writes a TeamCity service message to indicate
// that the compilation has finished with the given compiler.
func (t *ServiceMessage) CompilationFinished(compiler string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "compilationFinished",
		Attrs: MessageAttributes{
			"compiler": compiler,
		}.WithOptions(opts),
	})
}

// WithParentID returns an Option to set the parentId attribute of the message,
// which specifies the parent flow of the message.
// The message will be grouped under the parent flow in the build log.
func WithParentID(parentID string) Option {
	return func(attrs MessageAttributes) {
		attrs["parentId"] = parentID
	}
}

// FlowStarted writes a TeamCity service message to indicate
// the start of a flow with the given flowId.
func (t *ServiceMessage) FlowStarted(flowID string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "flowStarted",
		Attrs: MessageAttributes{
			"flowId": flowID,
		}.WithOptions(opts),
	})
}

// FlowFinished writes a TeamCity service message to indicate
// the end of a flow with the given flowId.
func (t *ServiceMessage) FlowFinished(flowID string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "flowFinished",
		Attrs: MessageAttributes{
			"flowId": flowID,
		}.WithOptions(opts),
	})
}

// TestSuiteStarted writes a TeamCity testSuiteStarted message for the given suite name.
func (t *ServiceMessage) TestSuiteStarted(name string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testSuiteStarted",
		Attrs: MessageAttributes{
			"name": name,
		}.WithOptions(opts),
	})
}

// TestSuiteFinished writes a TeamCity testSuiteFinished message for the given suite name.
func (t *ServiceMessage) TestSuiteFinished(name string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testSuiteFinished",
		Attrs: MessageAttributes{
			"name": name,
		}.WithOptions(opts),
	})
}

// TestStarted writes a TeamCity testStarted message for the given test name.
func (t *ServiceMessage) TestStarted(name string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testStarted",
		Attrs: MessageAttributes{
			"name": name,
		}.WithOptions(opts),
	})
}

// TestFinished writes a TeamCity testFinished message for the given test name.
func (t *ServiceMessage) TestFinished(name string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testFinished",
		Attrs: MessageAttributes{
			"name": name,
		}.WithOptions(opts),
	})
}

// TestFailed writes a TeamCity testFailed message for the given test name.
func (t *ServiceMessage) TestFailed(name string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testFailed",
		Attrs: MessageAttributes{
			"name": name,
		}.WithOptions(opts),
	})
}

// TestIgnored writes a TeamCity testIgnored message for the given test name.
func (t *ServiceMessage) TestIgnored(name string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testIgnored",
		Attrs: MessageAttributes{
			"name": name,
		}.WithOptions(opts),
	})
}

// TestStdOut writes a TeamCity testStdOut message for the given test name and output.
func (t *ServiceMessage) TestStdOut(name, out string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testStdOut",
		Attrs: MessageAttributes{
			"name": name,
			"out":  out,
		}.WithOptions(opts),
	})
}

// TestStdErr writes a TeamCity testStdErr message for the given test name and output.
func (t *ServiceMessage) TestStdErr(name, out string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testStdErr",
		Attrs: MessageAttributes{
			"name": name,
			"out":  out,
		}.WithOptions(opts),
	})
}

// TestRetrySupport writes a TeamCity testRetrySupport message to enable or disable retries.
func (t *ServiceMessage) TestRetrySupport(enabled bool, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "testRetrySupport",
		Attrs: MessageAttributes{
			"enabled": boolStr(enabled),
		}.WithOptions(opts),
	})
}

// InspectionType writes a TeamCity service message to define
// an inspection type with the given id, name, category and description.
// https://www.jetbrains.com/help/teamcity/service-messages.html#Inspection+Type
func (t *ServiceMessage) InspectionType(id, name, category, description string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "inspectionType",
		Attrs: MessageAttributes{
			"category":    category,
			"description": description,
			"id":          id,
			"name":        name,
		}.WithOptions(opts),
	})
}

// WithMessage returns an Option to set the message attribute of the message,
// which specifies the message text to be displayed in the build log.
func WithMessage(message string) Option {
	return func(attrs MessageAttributes) {
		attrs["message"] = message
	}
}

// WithLine returns an Option to set the line attribute of the message,
// which specifies the line number in the file where the inspection is located.
func WithLine(line int) Option {
	return func(attrs MessageAttributes) {
		attrs["line"] = fmt.Sprintf("%d", line)
	}
}

// WithSeverity returns an Option to set the SEVERITY attribute of the message,
// which specifies the severity of the message.
// The value can be one of INFO, ERROR, WARNING or WEAK WARNING.
func WithSeverity(severity string) Option {
	return func(attrs MessageAttributes) {
		attrs["SEVERITY"] = strings.ToUpper(severity)
	}
}

// Inspection writes a TeamCity service message to report an inspection with the given typeId and file.
// https://www.jetbrains.com/help/teamcity/service-messages.html#Inspection+Instance
func (t *ServiceMessage) Inspection(typeID, file string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "inspection",
		Attrs: MessageAttributes{
			"typeId": typeID,
			"file":   file,
		}.WithOptions(opts),
	})
}

// PublishArtifacts publishes the given path as a build artifact.
func (t *ServiceMessage) PublishArtifacts(path string) {
	t.WriteServiceMessage(&Message{
		Type:  "publishArtifacts",
		Attrs: MessageAttributes{"": path},
	})
}

// PublishNuGetPackage publishes NuGet packages produced in the current step.
func (t *ServiceMessage) PublishNuGetPackage(opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type:  "publishNuGetPackage",
		Attrs: MessageAttributes{}.WithOptions(opts),
	})
}

// WithCharset returns an Option to set the charset attribute of the message.
func WithCharset(charset string) Option {
	return func(attrs MessageAttributes) {
		attrs["charset"] = charset
	}
}

// WithWrapFileContentInBlock returns an Option to set the wrapFileContentInBlock attribute of the message.
func WithWrapFileContentInBlock(wrap bool) Option {
	return func(attrs MessageAttributes) {
		attrs["wrapFileContentInBlock"] = boolStr(wrap)
	}
}

// StreamToBuildLog writes a file to the build log.
// The file content will be streamed to the build log and can be optionally
// wrapped in a block to preserve formatting.
func (t *ServiceMessage) StreamToBuildLog(filePath string, opts ...Option) {
	t.ImportData("streamToBuildLog", append(opts, func(ma MessageAttributes) {
		ma["filePath"] = filePath
	})...)
}

// WithPath returns an Option to set the path attribute of the message,
// which specifies the path to the report file to be imported.
func WithPath(path string) Option {
	return func(attrs MessageAttributes) {
		attrs["path"] = path
	}
}

// ImportData writes a TeamCity importData message for the given report type and path.
func (t *ServiceMessage) ImportData(dataType string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "importData",
		Attrs: MessageAttributes{
			"type": dataType,
		}.WithOptions(opts),
	})
}

// WithSlackConnectionID set the connectionId attribute of the message,
// which specifies the Slack connection to use for sending the notification.
func WithSlackConnectionID(connectionID string) Option {
	return func(attrs MessageAttributes) {
		attrs["connectionId"] = connectionID
	}
}

// Slack writes a TeamCity service message to send a Slack notification with the given recipient and message body.
func (t *ServiceMessage) Slack(sendTo, message string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "notification",
		Attrs: MessageAttributes{
			"message":  message,
			"notifier": "slack",
			"sendTo":   sendTo,
		}.WithOptions(opts),
	})
}

// Email writes a TeamCity service message to send an email with the given address, subject and message.
func (t *ServiceMessage) Email(address, subject, message string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "notification",
		Attrs: MessageAttributes{
			"address":  address,
			"message":  message,
			"notifier": "email",
			"subject":  subject,
		}.WithOptions(opts),
	})
}

// WithReaddToQueue returns an Option to set the readdToQueue attribute of the message,
// which specifies whether the build should be re-added to the queue after being stopped.
func WithReaddToQueue(value bool) Option {
	return func(attrs MessageAttributes) {
		attrs["readdToQueue"] = boolStr(value)
	}
}

// BuildStop cancel a build with the given comment.
// The build will be stopped and optionally re-added to the queue based on the readdToQueue attribute.
func (t *ServiceMessage) BuildStop(comment string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "buildStop",
		Attrs: MessageAttributes{
			"comment": comment,
		}.WithOptions(opts),
	})
}

// WithTags returns an Option to set the tags attribute of the message,
// which specifies a comma-separated list of tags to be added to the build.
func WithTags(tags ...string) Option {
	return func(attrs MessageAttributes) {
		if len(tags) == 0 {
			return
		}
		attrs["tags"] = strings.Join(tags, ",")
	}
}

// SkipQueuedBuilds skips all queued builds with the given comment and tags.
func (t *ServiceMessage) SkipQueuedBuilds(comment string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "skipQueuedBuilds",
		Attrs: MessageAttributes{
			"comment": comment,
		}.WithOptions(opts),
	})
}

// AddBuildTag writes a TeamCity service message to add a build tag with the given tag.
func (t *ServiceMessage) AddBuildTag(tag string) {
	t.WriteServiceMessage(&Message{
		Type:  "addBuildTag",
		Attrs: MessageAttributes{"": tag},
	})
}

// RemoveBuildTag writes a TeamCity service message to remove a build tag with the given tag.
func (t *ServiceMessage) RemoveBuildTag(tag string) {
	t.WriteServiceMessage(&Message{
		Type:  "removeBuildTag",
		Attrs: MessageAttributes{"": tag},
	})
}

// UndoPersonalPatch writes a TeamCity service message to undo the personal patch of the build.
func (t *ServiceMessage) UndoPersonalPatch() {
	t.WriteServiceMessage(&Message{
		Type: "undoPersonalPatch",
	})
}

// SetParameter writes a TeamCity service message to set a parameter with the given key and value.
func (t *ServiceMessage) SetParameter(name, value string) {
	t.WriteServiceMessage(&Message{
		Type: "setParameter",
		Attrs: MessageAttributes{
			"name":  name,
			"value": value,
		},
	})
}

// WithIdentity returns an Option to set the identity attribute of the message.
func WithIdentity(value string) Option {
	return func(attrs MessageAttributes) {
		attrs["identity"] = value
	}
}

// BuildProblem writes a TeamCity service message to report a build problem with the given description.
func (t *ServiceMessage) BuildProblem(desc string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "buildProblem",
		Attrs: MessageAttributes{
			"description": desc,
		}.WithOptions(opts),
	})
}

// BuildStatus writes a TeamCity service message to set the build status with the given text.
func (t *ServiceMessage) BuildStatus(text string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "buildStatus",
		Attrs: MessageAttributes{
			"text": text,
		}.WithOptions(opts),
	})
}

// BuildNumber writes a TeamCity buildNumber message with the provided value.
func (t *ServiceMessage) BuildNumber(number string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "buildNumber",
		Attrs: MessageAttributes{
			"": number,
		}.WithOptions(opts),
	})
}

// BuildStatisticValue writes a TeamCity buildStatisticValue message for the provided key and value.
func (t *ServiceMessage) BuildStatisticValue(key, value string, opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type: "buildStatisticValue",
		Attrs: MessageAttributes{
			"key":   key,
			"value": value,
		}.WithOptions(opts),
	})
}

// EnableServiceMessages writes a TeamCity enableServiceMessages message.
func (t *ServiceMessage) EnableServiceMessages(opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type:  "enableServiceMessages",
		Attrs: MessageAttributes{}.WithOptions(opts),
	})
}

// DisableServiceMessages writes a TeamCity disableServiceMessages message.
func (t *ServiceMessage) DisableServiceMessages(opts ...Option) {
	t.WriteServiceMessage(&Message{
		Type:  "disableServiceMessages",
		Attrs: MessageAttributes{}.WithOptions(opts),
	})
}

// WriteServiceMessage writes the given service message to the output.
func (t *ServiceMessage) WriteServiceMessage(msg *Message) {
	if _, err := fmt.Fprintln(t.w, msg.String()); err != nil {
		panic(fmt.Errorf("failed to write message: %w", err))
	}
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
