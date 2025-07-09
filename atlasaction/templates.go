//go:build manifest
// +build manifest

package atlasaction

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

//go:embed templates/*
var templateDir embed.FS

var (
	// templates holds the Go templates for the code generation.
	templates *Template
	// Funcs are the predefined template
	// functions used by the codegen.
	Funcs = template.FuncMap{
		"xtemplate":   xtemplate,
		"hasTemplate": hasTemplate,
		"trimnl": func(s string) string {
			return strings.Trim(s, "\n")
		},
		"nl2sp": func(s string) string {
			return strings.ReplaceAll(s, "\n", " ")
		},
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
		"yaml": func(spaces int, v any) (string, error) {
			var buf bytes.Buffer
			e := yaml.NewEncoder(&buf)
			e.SetIndent(spaces)
			if err := e.Encode(v); err != nil {
				return "", err
			}
			return buf.String(), nil
		},
		"jsonIndent": func(prefix, indent string, v any) (string, error) {
			b, err := json.MarshalIndent(v, prefix, indent)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
		"replace": func(old, new, s string) string {
			return strings.ReplaceAll(s, old, new)
		},
		"env":       toEnvName,
		"inputvar":  toInputVarName,
		"outputvar": toOutputVarName,
		"dockers": func() []DockerURL {
			return []DockerURL{
				{Label: "MySQL", Driver: "mysql"},
				{Label: "Postgres", Driver: "postgres"},
				{Label: "MariaDB", Driver: "mariadb"},
				{Label: "SQL Server", Driver: "sqlserver"},
				{Label: "ClickHouse", Driver: "clickhouse"},
				{Label: "SQLite", Driver: "sqlite"},
			}
		},
	}
)

func init() {
	LoadTemplates(templateDir, "templates/*")
}

// LoadTemplates loads the templates from the given file system.
func LoadTemplates(fsys fs.FS, patterns ...string) *Template {
	templates = MustParse(NewTemplate("templates").
		ParseFS(fsys, patterns...))
	return templates
}

type DockerURL struct {
	Label  string
	Driver string
}

func (d DockerURL) DevURL() string {
	switch d.Driver {
	case "mysql":
		return "docker://mysql/8/dev"
	case "postgres":
		return "docker://postgres/15/dev?search_path=public"
	case "mariadb":
		return "docker://maria/latest/schema"
	case "sqlserver":
		return "docker://sqlserver/2022-latest?mode=schema"
	case "clickhouse":
		return "docker://clickhouse/23.11/dev"
	case "sqlite":
		return "sqlite://db?mode=memory"
	}
	return ""
}

type Template struct {
	*template.Template
	FuncMap template.FuncMap
}

// MustParse is a helper that wraps a call to a function returning (*Template, error)
// and panics if the error is non-nil.
func MustParse(t *Template, err error) *Template {
	if err != nil {
		panic(err)
	}
	return t
}

// NewTemplate creates an empty template with the standard codegen functions.
func NewTemplate(name string) *Template {
	t := &Template{Template: template.New(name)}
	return t.Funcs(Funcs)
}

// ParseFS is like ParseFiles or ParseGlob but reads from the file system fsys
// instead of the host operating system's file system.
func (t *Template) ParseFS(fsys fs.FS, patterns ...string) (*Template, error) {
	if _, err := t.Template.ParseFS(fsys, patterns...); err != nil {
		return nil, err
	}
	return t, nil
}

// Funcs merges the given funcMap with the template functions.
func (t *Template) Funcs(funcMap template.FuncMap) *Template {
	t.Template.Funcs(funcMap)
	if t.FuncMap == nil {
		t.FuncMap = template.FuncMap{}
	}
	for name, f := range funcMap {
		if _, ok := t.FuncMap[name]; !ok {
			t.FuncMap[name] = f
		}
	}
	return t
}

// xtemplate dynamically executes templates by their names.
func xtemplate(name string, v any) (string, error) {
	buf := bytes.NewBuffer(nil)
	if err := templates.ExecuteTemplate(buf, name, v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// hasTemplate checks whether a template exists in the loaded templates.
func hasTemplate(name string) bool {
	for _, t := range templates.Templates() {
		if t.Name() == name {
			return true
		}
	}
	return false
}
