package git

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseGitConflicts(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []Conflict
	}{
		{
			name: "Single conflict",
			input: `<<<<<<< branch_a
content from branch_a
=======
content from branch_b
>>>>>>> branch_b`,
			expected: []Conflict{
				{
					Base:     "content from branch_a",
					Incoming: "content from branch_b",
				},
			},
		},
		{
			name: "Multiple conflicts",
			input: `<<<<<<< branch_a
first conflict branch_a
=======
first conflict branch_b
>>>>>>> branch_b
normal line
<<<<<<< branch_a
second conflict branch_a
=======
second conflict branch_b
>>>>>>> branch_b`,
			expected: []Conflict{
				{
					Base:     "first conflict branch_a",
					Incoming: "first conflict branch_b",
				},
				{
					Base:     "second conflict branch_a",
					Incoming: "second conflict branch_b",
				},
			},
		},
		{
			name:     "No conflicts",
			input:    "This is a normal file with no conflicts",
			expected: []Conflict{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, ParseConflicts(tt.input))
		})
	}
}

func TestFilesOnlyInBase(t *testing.T) {
	tests := []struct {
		name     string
		conflict Conflict
		want     []string
	}{
		{
			name: "1 unique file only in base",
			conflict: Conflict{
				Base: `h1:GplzCB5bzYwaRyf6zllMDN5xUpp139MxS/9lPRBbXwg=
20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=
20250309093929_test.sql h1:zbRBfc5QvPTMZnEzN+JgIoClApvV+nB+xhZ3mU7jU90=`,
				Incoming: `h1:OBdzlZYBlTgWANMK27EiJUZeVVT/SYmbNYRC0QA31LE=
20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=
20250309093957_test_1.sql h1:hPxhaSbvanrHft0l8BTxVZe+284tY68vJh3hDV7YxHs=`,
			},
			want: []string{"20250309093929_test.sql"},
		},
		{
			name: "2 unique file only in base",
			conflict: Conflict{
				Base: `h1:GplzCB5bzYwaRyf6zllMDN5xUpp139MxS/9lPRBbXwg=
20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=
20250309093929_test.sql   h1:zbRBfc5QvPTMZnEzN+JgIoClApvV+nB+xhZ3mU7jU90=
20250309093400_test_2.sql h1:zbRBfc5QvPTMZnEzN+JgIoClApvV+nB+xhZ3mU7jU90=`,
				Incoming: `h1:OBdzlZYBlTgWANMK27EiJUZeVVT/SYmbNYRC0QA31LE=
20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=
20250309093957_test_1.sql h1:hPxhaSbvanrHft0l8BTxVZe+284tY68vJh3hDV7YxHs=`,
			},
			want: []string{"20250309093929_test.sql", "20250309093400_test_2.sql"},
		},
		{
			name: "No unique files in base",
			conflict: Conflict{
				Base:     "file1.sql h1:hash\nfile2.sql h1:hash",
				Incoming: "file1.sql h1:hash\nfile2.sql h1:hash",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.conflict.FilesOnlyInBase())
		})
	}
}
