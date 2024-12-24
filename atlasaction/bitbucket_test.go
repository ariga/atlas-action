package atlasaction_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

func TestBitbucketPipe(t *testing.T) {
	var (
		actions = "actions"
		outputs = filepath.Join("outputs.sh")
	)
	wd, err := os.Getwd()
	require.NoError(t, err)
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "bitbucket"),
		Setup: func(e *testscript.Env) (err error) {
			dir := filepath.Join(e.WorkDir, actions)
			if err := os.Mkdir(dir, 0700); err != nil {
				return err
			}
			e.Setenv("MOCK_ATLAS", filepath.Join(wd, "mock-atlas.sh"))
			e.Setenv("BITBUCKET_PIPELINE_UUID", "fbfb4205-c666-42ed-983a-d27f47f2aad2")
			e.Setenv("BITBUCKET_PIPE_STORAGE_DIR", dir)
			e.Setenv("BITBUCKET_CLONE_DIR", e.WorkDir)
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"output": func(ts *testscript.TestScript, neg bool, args []string) {
				if len(args) == 0 {
					_, err := os.Stat(ts.MkAbs(outputs))
					if neg {
						if !os.IsNotExist(err) {
							ts.Fatalf("expected no output, but got some")
						}
						return
					}
					if err != nil {
						ts.Fatalf("expected output, but got none")
						return
					}
					return
				}
				cmpFiles(ts, neg, args[0], outputs)
			},
		},
	})
}
