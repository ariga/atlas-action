package main

import (
	"embed"
	"io"
	"os"

	"ariga.io/atlas-action/atlasaction/gen"
)

//go:embed template/*
var templateDir embed.FS

func main() {
	if len(os.Args) != 2 {
		panic("gen: invalid arguments")
	}
	acts, err := gen.ParseFS(os.DirFS(os.Args[1]))
	if err != nil {
		panic(err)
	}
	err = readmeMarkdown(os.Stdout, acts.Actions)
	if err != nil {
		panic(err)
	}
}

func readmeMarkdown(w io.Writer, actions []gen.ActionSpec) error {
	return gen.LoadTemplates(templateDir, "template/*.md").
		ExecuteTemplate(w, "bitbucket.md", actions)
}
