package atlasaction_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

// Users source the outputs file. Sourcing must restore each value byte for
// byte and run nothing. CircleCI writes the same format to $BASH_ENV, see
// testdata/circleci/migrate-drift.txtar.
func TestBitbucketSourcedOutputs(t *testing.T) {
	for _, v := range []string{
		"",
		"it's",
		`"double"`,
		"CREATE TABLE `t` (`c` int)",
		"x$(echo INJECTED)",
		"$$ $1 $HOME ${HOME}",
		`back\slash \n`,
		"line1\nline2\n",
		"Xin chào",
		`'\''`,
	} {
		dir := t.TempDir()
		env := map[string]string{"ATLAS_OUTPUT_DIR": dir, "ATLAS_ACTION_COMMAND": "migrate/drift"}
		atlasaction.NewBitBucket(func(k string) string { return env[k] }, io.Discard).SetOutput("report", v)
		cmd := exec.Command("bash", "-c", `source "$1" && printf %s "$ATLAS_OUTPUT_MIGRATE_DRIFT_REPORT"`,
			"bash", filepath.Join(dir, "outputs.sh"))
		cmd.Env = []string{} // Keep the host's BASH_ENV and SHELLOPTS out.
		got, err := cmd.CombinedOutput()
		require.NoError(t, err, string(got))
		require.Equal(t, v, string(got))
	}
}

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
