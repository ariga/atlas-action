// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"io"
	"testing"
	"time"

	"ariga.io/atlas/sql/migrate"
	"github.com/stretchr/testify/require"
)

func TestVersionTime(t *testing.T) {
	for _, tt := range []struct {
		name string
		want string // Empty if the version is not a timestamp.
	}{
		{name: "20261123232323_a.sql", want: "2026-11-23T23:23:24Z"},
		{name: "V20261123232323__a.sql", want: "2026-11-23T23:23:24Z"},
		{name: "20261123235959_a.sql", want: "2026-11-24T00:00:00Z"},
		// Versions that `atlas migrate rebase` increments past the end of a field.
		{name: "20261123232360_a.sql", want: "2026-11-23T23:24:00Z"},
		{name: "20261123232392_a.sql", want: "2026-11-23T23:24:00Z"},
		{name: "20261123235992_a.sql", want: "2026-11-24T00:00:00Z"},
		{name: "20261123236000_a.sql", want: "2026-11-24T00:00:00Z"},
		{name: "20261123240000_a.sql", want: "2026-11-24T00:00:00Z"},
		{name: "20261131000000_a.sql", want: "2026-12-01T00:00:00Z"},
		{name: "20261231235960_a.sql", want: "2027-01-01T00:00:00Z"},
		{name: "1_init.up.sql"},
		{name: "20261323000000_a.sql"},
		{name: "2026112323232_a.sql"},
	} {
		got, ok := versionTime(tt.name)
		if tt.want == "" {
			require.False(t, ok, tt.name)
			continue
		}
		require.True(t, ok, tt.name)
		require.Equal(t, tt.want, got.Format(time.RFC3339), tt.name)
		// The version of the time sorts after the one of the file, and the one of a second earlier does not.
		v := tt.name[:14]
		if tt.name[0] == 'V' {
			v = tt.name[1:15]
		}
		require.Greater(t, got.Format("20060102150405"), v, tt.name)
		require.LessOrEqual(t, got.Add(-time.Second).Format("20060102150405"), v, tt.name)
	}
}

func TestWaitClock(t *testing.T) {
	var (
		now   = time.Date(2026, 11, 23, 23, 23, 0, 0, time.UTC)
		slept []time.Duration
		r     = &restacker{
			Actions: &Actions{Action: NewGitHub(func(string) string { return "" }, io.Discard)},
			now:     func() time.Time { return now },
			sleep: func(_ context.Context, d time.Duration) error {
				slept = append(slept, d)
				return nil
			},
		}
		sum = func(name string) migrate.HashFile { return migrate.HashFile{{N: "20260101000000_init.sql"}, {N: name}} }
	)
	// Behind the clock, or not a timestamp.
	require.NoError(t, r.waitClock(context.Background(), sum("20261123232259_a.sql"), "main", false))
	require.NoError(t, r.waitClock(context.Background(), sum("1_init.up.sql"), "main", false))
	require.NoError(t, r.waitClock(context.Background(), nil, "main", false))
	require.Empty(t, slept)
	// Ahead of the clock.
	require.NoError(t, r.waitClock(context.Background(), sum("20261123232305_a.sql"), "main", false))
	require.Equal(t, []time.Duration{6 * time.Second}, slept)
	// Written by `atlas migrate rebase` for many files.
	slept = nil
	require.NoError(t, r.waitClock(context.Background(), sum("20261123232392_a.sql"), "#1 (A)", true))
	require.Equal(t, []time.Duration{time.Minute}, slept)
	// Far ahead, and not written by this run.
	slept = nil
	err := r.waitClock(context.Background(), sum("20261124000000_a.sql"), "main", false)
	require.EqualError(t, err, "the latest migration of main, 20261124000000_a.sql, is versioned 37m1s ahead of the clock")
	require.Empty(t, slept)
	// Far ahead, but written by this run.
	require.NoError(t, r.waitClock(context.Background(), sum("20261124000000_a.sql"), "#1 (A)", true))
	require.Equal(t, []time.Duration{37*time.Minute + time.Second}, slept)
	// Behind the clock, but this run wrote a later version for another stack.
	slept, r.last = nil, "20261123232310_x.sql"
	require.NoError(t, r.waitClock(context.Background(), sum("20261123232259_a.sql"), "main", false))
	require.Equal(t, []time.Duration{11 * time.Second}, slept)
	// Both are ahead, and the later one is waited for.
	slept = nil
	require.NoError(t, r.waitClock(context.Background(), sum("20261123232320_a.sql"), "main", false))
	require.Equal(t, []time.Duration{21 * time.Second}, slept)
}

func TestVersions(t *testing.T) {
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
	// Other directory formats.
	r.atlas = false
	require.True(t, r.onTop(p, sum("1_a.sql", "20260105000000_x1.sql", "20260105000000_y1.sql"), true))

	require.True(t, versionAfter("20261123222860_a.sql", "20261123222859_b.sql"))
	require.True(t, versionAfter("10_a.sql", "9_b.sql"))
	require.False(t, versionAfter("9_a.sql", "10_b.sql"))
	require.False(t, versionAfter("1_b.sql", "1_a.sql"))
}

func TestGitCommand(t *testing.T) {
	require.Equal(t, "rebase", gitCommand([]string{"-c", "merge.atlas-sum.driver=true", "rebase", "--onto", "a", "b"}))
	require.Equal(t, "push", gitCommand([]string{"push", "--atomic"}))
	require.Equal(t, "--version", gitCommand([]string{"--version"}))
}
