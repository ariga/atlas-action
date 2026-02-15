// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package teamcity_test

import (
	"testing"

	"ariga.io/atlas-action/internal/teamcity"
	"github.com/stretchr/testify/require"
)

func TestMessage(t *testing.T) {
	t.Run("No attributes", func(t *testing.T) {
		m := teamcity.Message{
			Type:  "message",
			Attrs: map[string]string{},
		}
		require.Equal(t, "##teamcity[message]", m.String())
	})
	t.Run("Single value", func(t *testing.T) {
		m := teamcity.Message{
			Type: "message",
			Attrs: map[string]string{
				"": "value",
			},
		}
		require.Equal(t, "##teamcity[message 'value']", m.String())
	})
	t.Run("With Attributes", func(t *testing.T) {
		m := teamcity.Message{
			Type: "message",
			Attrs: map[string]string{
				"key2": "value2",
				"key1": "value1",
			},
		}
		require.Equal(t, "##teamcity[message key1='value1' key2='value2']", m.String())
	})
}

func TestTeamCity_escapeString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic ASCII",
			input:    "hello world",
			expected: "##teamcity[message text='hello world']",
		},
		{
			name:     "newline",
			input:    "hello\nworld",
			expected: "##teamcity[message text='hello|nworld']",
		},
		{
			name:     "special chars",
			input:    "hello|world['test']",
			expected: "##teamcity[message text='hello||world|[|'test|'|]']",
		},
		{
			name:     "BMP unicode character (U+00E9 - é)",
			input:    "café",
			expected: "##teamcity[message text='caf|0x00E9']",
		},
		{
			name:     "BMP unicode character (U+1F60 - ὠ)",
			input:    "ὠ",
			expected: "##teamcity[message text='|0x1F60']",
		},
		{
			name:     "non-BMP emoji (U+1F600 - 😀)",
			input:    "😀",
			expected: "##teamcity[message text='|0xD83D|0xDE00']", // UTF-16 surrogate pair
		},
		{
			name:     "non-BMP emoji (U+1F680 - 🚀)",
			input:    "🚀",
			expected: "##teamcity[message text='|0xD83D|0xDE80']", // UTF-16 surrogate pair
		},
		{
			name:     "mixed ASCII and non-BMP",
			input:    "hello 😀 world",
			expected: "##teamcity[message text='hello |0xD83D|0xDE00 world']",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := teamcity.Message{
				Type: "message",
				Attrs: map[string]string{
					"text": tt.input,
				},
			}
			require.Equal(t, tt.expected, m.String())
		})
	}
}
