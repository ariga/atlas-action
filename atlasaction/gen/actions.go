package gen

import (
	"cmp"
	"errors"
	"io/fs"
	"iter"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type (
	// Actions represents a list of actions.
	Actions struct {
		Actions []ActionSpec
	}
	ActionSpec struct {
		ID          string                  `yaml:"id"`
		Description string                  `yaml:"description"`
		Name        string                  `yaml:"name"`
		Inputs      map[string]ActionInput  `yaml:"inputs,omitempty"`
		Outputs     map[string]ActionOutput `yaml:"outputs,omitempty"`
	}
	ActionInput struct {
		Description string `yaml:"description,omitempty"`
		Default     string `yaml:"default,omitempty"`
		Required    bool   `yaml:"required"`
	}
	ActionOutput struct {
		Description string `yaml:"description"`
	}
)

// ParseFS parses the actions from the given file system.
func ParseFS(fsys fs.FS) (*Actions, error) {
	level1, err := fs.Glob(fsys, "**/*/action.yml")
	if err != nil {
		return nil, err
	}
	level2, err := fs.Glob(fsys, "**/*/*/action.yml")
	if err != nil {
		return nil, err
	}
	files := append(level1, level2...)
	actions := make([]ActionSpec, len(files))
	for i, f := range files {
		if err := actions[i].fromPath(fsys, f); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(actions, func(i, j ActionSpec) int {
		return cmp.Compare(i.ID, j.ID)
	})
	return &Actions{Actions: actions}, nil
}

func (a *ActionSpec) fromPath(fsys fs.FS, path string) error {
	f, err := fsys.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = yaml.NewDecoder(f).Decode(a); err != nil {
		return err
	}
	if id, ok := strings.CutSuffix(path, "/action.yml"); ok {
		a.ID = strings.Trim(id, "/")
		return nil
	}
	return errors.New("gen: invalid action.yml path")
}

func (a *ActionSpec) SortedInputs() iter.Seq2[string, ActionInput] {
	orders := []string{"working-directory", "config", "env", "vars", "dev-url"}
	return func(yield func(string, ActionInput) bool) {
		keys := slices.SortedFunc(maps.Keys(a.Inputs), ComparePriority(orders))
		for _, k := range keys {
			if !yield(k, a.Inputs[k]) {
				return
			}
		}
	}
}

func (a *ActionSpec) SortedOutputs() iter.Seq2[string, ActionOutput] {
	return func(yield func(string, ActionOutput) bool) {
		keys := slices.Sorted(maps.Keys(a.Outputs))
		for _, k := range keys {
			if !yield(k, a.Outputs[k]) {
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
