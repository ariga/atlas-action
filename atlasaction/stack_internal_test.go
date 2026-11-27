// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"ariga.io/atlas/sql/migrate"
	"github.com/stretchr/testify/require"
)

func TestWaitClock(t *testing.T) {
	var (
		start = time.Date(2026, 11, 23, 23, 23, 0, 0, time.UTC)
		r     = &restacker{Actions: &Actions{Action: NewGitHub(func(string) string { return "" }, io.Discard)}}
		// wait runs waitClock from the start time, on the fake clock of a synctest bubble, and returns how long it waited.
		wait = func(names ...string) (d time.Duration, err error) {
			synctest.Test(t, func(t *testing.T) {
				time.Sleep(time.Until(start))
				err = r.waitClock(t.Context(), append([]string{"20260101000000_init.sql"}, names...), "main")
				d = time.Since(start)
			})
			return d, err
		}
	)
	// Behind the clock, or not a timestamp.
	for _, names := range [][]string{{"20261123232259_a.sql"}, {"1_init.up.sql"}, {"3_a.sql"}, nil} {
		d, err := wait(names...)
		require.NoError(t, err, names)
		require.Zero(t, d, names)
	}
	// Ahead of the clock.
	d, err := wait("20261123232305_a.sql")
	require.NoError(t, err)
	require.Equal(t, 6*time.Second, d)
	// Versions that `atlas migrate rebase` increments past the end of a field, which sort before the next minute.
	d, err = wait("20261123232359_a.sql", "20261123232392_b.sql")
	require.NoError(t, err)
	require.Equal(t, time.Minute, d)
	// Too far ahead.
	for _, name := range []string{"20261123232800_a.sql", "20261124000000_a.sql", "20301123232392_a.sql"} {
		d, err = wait(name)
		require.EqualError(t, err, "the latest migration of main, "+name+", is versioned more than 5m0s ahead of the clock")
		require.Zero(t, d)
	}
	d, err = wait("20261123232759_a.sql")
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, d)
	// The run is canceled.
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(start))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, r.waitClock(ctx, []string{"20260101000000_init.sql", "20261123232305_a.sql"}, "main"), context.Canceled)
	})
}

func TestOnTop(t *testing.T) {
	r := &restacker{atlas: true}
	sum := func(names ...string) migrate.HashFile {
		hf := make(migrate.HashFile, len(names))
		for i, n := range names {
			hf[i].N, hf[i].H = n, "h"+n
		}
		return hf
	}
	p := sum("1_a.sql", "20260105000000_x1.sql")
	require.True(t, r.onTop(p, p, true))
	require.True(t, r.onTop(p, sum("1_a.sql", "20260105000000_x1.sql", "20260105000001_y1.sql"), true))
	// The same version as a file of the parent.
	require.False(t, r.onTop(p, sum("1_a.sql", "20260105000000_x1.sql", "20260105000000_y1.sql"), true))
	// Atlas orders the files by name, so a version that sorts after the others is on top.
	require.True(t, r.onTop(sum("1_a.sql", "20_b.sql"), sum("1_a.sql", "20_b.sql", "3_c.sql"), true))
	// Other hashes.
	c := sum("1_a.sql", "20260105000000_x1.sql", "20260106000000_y1.sql")
	c[1].H = "edited"
	require.False(t, r.onTop(p, c, true))
	require.True(t, r.onTop(p, c, false))
	require.False(t, r.onTop(p, sum("1_a.sql"), false))
	// Other directory formats cannot have two files of a version either, and their files without a version,
	// like the repeatable migrations of Flyway, are left out of the sums.
	r.atlas = false
	require.False(t, r.onTop(p, sum("1_a.sql", "20260105000000_x1.sql", "20260105000000_y1.sql"), true))
	require.False(t, r.onTop(sum("V1__a.sql", "V2__m.sql"), sum("V1__a.sql", "V2__m.sql", "V2__z.sql"), true))
	require.Equal(t, sum("V1__a.sql", "V2__m.sql"), r.versioned(sum("V1__a.sql", "V2__m.sql", "R__a.sql", "R__b.sql")))
	r.atlas = true
	require.Equal(t, sum("1_a.sql", "R__a.sql"), r.versioned(sum("1_a.sql", "R__a.sql")))
}

func TestGitCommand(t *testing.T) {
	require.Equal(t, "rebase", gitCommand([]string{"-c", "merge.atlas-sum.driver=true", "rebase", "--onto", "a", "b"}))
	require.Equal(t, "push", gitCommand([]string{"push", "--atomic"}))
	require.Equal(t, "--version", gitCommand([]string{"--version"}))
}
