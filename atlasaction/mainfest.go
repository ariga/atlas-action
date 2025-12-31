// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

//go:build manifest
// +build manifest

package atlasaction

import (
	"bytes"
	"cmp"
	_ "embed"
	"fmt"
	"io"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed manifest.yml
var manifest []byte

type (
	// Actions represents a list of actions.
	ActionsManifest struct {
		Actions []ActionSpec
	}
	ActionSpec struct {
		ID          string                  `yaml:"id"`
		Name        string                  `yaml:"name"`
		Description string                  `yaml:"description"`
		Inputs      map[string]ActionInput  `yaml:"inputs,omitempty"`
		Outputs     map[string]ActionOutput `yaml:"outputs,omitempty"`
	}
	ActionInput struct {
		Type        string   `yaml:"type"`                  // e.g., "string", "boolean", "number", "enum", etc.
		MultiLine   bool     `yaml:"multiLine,omitempty"`   // Indicates if the input accepts multiple lines (e.g., for lists)
		Default     string   `yaml:"default,omitempty"`     // Default value for the input
		Options     []string `yaml:"options,omitempty"`     // For enum inputs
		Required    bool     `yaml:"required,omitempty"`    // Is this input required?
		Label       string   `yaml:"label,omitempty"`       // Label for the input field
		Description string   `yaml:"description,omitempty"` // Markdown description of the input
	}
	ActionOutput struct {
		Type        string `yaml:"type,omitempty"` // e.g., "string", "boolean", "number", etc.
		Description string `yaml:"description"`
	}
)

// ParseManifest reads the actions from the given reader and returns an Actions struct.
// It expects the input to be in YAML format.
func ParseManifest() (*ActionsManifest, error) {
	var actions ActionsManifest
	err := yaml.NewDecoder(bytes.NewReader(manifest)).Decode(&actions)
	if err != nil {
		return nil, err
	}
	return &actions, nil
}

func (a ActionsManifest) AsOptions() map[string]string {
	opts := make(map[string]string, len(a.Actions))
	for _, act := range a.Actions {
		opts[strings.ReplaceAll(act.ID, "/", " ")] = act.Name
	}
	return opts
}

func (a ActionSpec) SortedInputs() iter.Seq2[string, ActionInput] {
	return SortedInputs(a.Inputs, []string{
		"working-directory", "config", "env", "vars", "dev-url",
	})
}

func (a ActionSpec) SortedOutputs() iter.Seq2[string, ActionOutput] {
	return func(yield func(string, ActionOutput) bool) {
		keys := slices.Sorted(maps.Keys(a.Outputs))
		for _, k := range keys {
			if !yield(k, a.Outputs[k]) {
				return
			}
		}
	}
}

// AzureInputs returns a sequence of AzureInputGroups, which groups action inputs by their keys.
func (a ActionsManifest) AzureInputs() iter.Seq2[string, AzureInputGroups] {
	inputs := make(map[string]AzureInputGroups)
	for _, act := range a.Actions {
		for k, v := range act.Inputs {
			i, ok := inputs[k]
			if !ok {
				i.ActionInput = v
			}
			i.Groups = append(i.Groups, act.ID)
			inputs[k] = i
		}
	}
	return SortedInputs(inputs, []string{
		"working-directory", "config", "env", "vars", "dev-url",
	})
}

type AzureInputGroups struct {
	ActionInput
	Groups []string
}

func (a AzureInputGroups) VisibleRule() string {
	slices.Sort(a.Groups)
	rule := make([]string, 0, len(a.Groups))
	for _, g := range a.Groups {
		rule = append(rule, "action == "+strings.ReplaceAll(g, "/", " "))
	}
	return strings.Join(rule, " || ")
}

// AzureInputType returns the Azure DevOps input type for the action input.
func (a ActionInput) AzureInputType() string {
	switch a.Type {
	case "boolean":
		return "boolean"
	case "number":
		return "int"
	case "string":
		if a.MultiLine {
			return "multiLine"
		}
		if len(a.Options) > 0 {
			return "pickList"
		}
		fallthrough
	default:
		return "string"
	}
}

// GitHubManifests writes the actions to the given path as GitHub Actions manifests.
// It creates a directory for each action with the action ID as the name and writes
// the action.yml file inside it. The action.yml file is generated using the
// "action-yml.tmpl" template.
func (a ActionsManifest) GitHubManifests(path string) error {
	write := func(a ActionSpec) error {
		dir := filepath.Join(path, a.ID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
		file, err := os.OpenFile(filepath.Join(dir, "action.yml"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("creating action.yml in %s: %w", dir, err)
		}
		defer file.Close()
		return templates.ExecuteTemplate(file, "action-yml.tmpl", a)
	}
	for _, act := range a.Actions {
		if act.ID == "" {
			continue
		}
		if err := write(act); err != nil {
			return fmt.Errorf("writing action %s: %w", act.ID, err)
		}
	}
	return nil
}

func (a ActionsManifest) GitLabTemplates(path string) error {
	write := func(a ActionSpec) error {
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", path, err)
		}
		temp := filepath.Join(path, strings.ReplaceAll(a.ID, "/", "-"))
		file, err := os.OpenFile(fmt.Sprintf("%s.yml", temp), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("creating %s: %w", temp, err)
		}
		defer file.Close()
		return templates.ExecuteTemplate(file, "gitlab-yml.tmpl", a)
	}
	for _, act := range a.Actions {
		if act.ID == "" {
			continue
		}
		if err := write(act); err != nil {
			return fmt.Errorf("writing action %s: %w", act.ID, err)
		}
	}
	return nil
}

// AzureTaskJSON writes the actions to the given path as an Azure DevOps task JSON file.
// It creates a file with the given path and writes the action data using the
// "azure-task-json.tmpl" template.
func (a ActionsManifest) AzureTaskJSON(w io.Writer) error {
	return templates.ExecuteTemplate(w, "task-json.tmpl", a)
}

// MarkdownDocs writes the actions to the given path as Markdown documentation files.
func (a ActionsManifest) MarkdownDocs(path string) error {
	write := func(doc string) error {
		f, err := os.OpenFile(filepath.Join(path, doc), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("opening file %s: %w", path, err)
		}
		defer f.Close()
		return templates.ExecuteTemplate(f, doc, a)
	}
	for _, doc := range []string{
		"azure.mdx",
		"bitbucket.mdx",
	} {
		if err := write(doc); err != nil {
			return fmt.Errorf("writing doc %s: %w", doc, err)
		}
	}
	return nil
}

func SortedInputs[Map ~map[K]V, K cmp.Ordered, V any](m Map, orders []K) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		keys := slices.SortedFunc(maps.Keys(m), ComparePriority(orders))
		for _, k := range keys {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}

func ComparePriority[T cmp.Ordered](ordered []T) func(T, T) int {
	return func(x, y T) int {
		switch xi, yi := slices.Index(ordered, x), slices.Index(ordered, y); {
		case xi != -1 && yi != -1:
			return cmp.Compare(xi, yi)
		case yi != -1:
			return -1
		case xi != -1:
			return +1
		}
		// fallback to default comparison
		return cmp.Compare(x, y)
	}
}
