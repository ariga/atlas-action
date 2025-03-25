package git

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilesOnlyInBase(t *testing.T) {
	tests := []struct {
		name           string
		base, incoming string
		want           []string
	}{
		{
			name: "file name have only timestamp",
			base: `h1:GplzCB5bzYwaRyf6zllMDN5xUpp139MxS/9lPRBbXwg=
	  20250309093454.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
`,
			incoming: `h1:OBdzlZYBlTgWANMK27EiJUZeVVT/SYmbNYRC0QA31LE=
	2025030900000.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
`,
			want: []string{"20250309093454.sql"},
		},
		{
			name: "1 unique file only in base",
			base: `h1:GplzCB5bzYwaRyf6zllMDN5xUpp139MxS/9lPRBbXwg=
20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
  20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=
20250309093929_test.sql h1:zbRBfc5QvPTMZnEzN+JgIoClApvV+nB+xhZ3mU7jU90=`,
			incoming: `h1:OBdzlZYBlTgWANMK27EiJUZeVVT/SYmbNYRC0QA31LE=
	20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
  20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=
20250309093957_test_1.sql h1:hPxhaSbvanrHft0l8BTxVZe+284tY68vJh3hDV7YxHs=`,
			want: []string{"20250309093929_test.sql"},
		},
		{
			name: "2 unique file only in base",
			base: `h1:GplzCB5bzYwaRyf6zllMDN5xUpp139MxS/9lPRBbXwg=
20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=
20250309093929_test.sql   h1:zbRBfc5QvPTMZnEzN+JgIoClApvV+nB+xhZ3mU7jU90=
20250309093400_test_2.sql h1:zbRBfc5QvPTMZnEzN+JgIoClApvV+nB+xhZ3mU7jU90=`,
			incoming: `h1:OBdzlZYBlTgWANMK27EiJUZeVVT/SYmbNYRC0QA31LE=
20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=
20250309093957_test_1.sql h1:hPxhaSbvanrHft0l8BTxVZe+284tY68vJh3hDV7YxHs=`,
			want: []string{"20250309093929_test.sql", "20250309093400_test_2.sql"},
		},
		{
			name:     "No unique files in base",
			base:     "file1.sql h1:hash\nfile2.sql h1:hash",
			incoming: "file1.sql h1:hash\nfile2.sql h1:hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, FilesOnlyInBase(tt.base, tt.incoming))
		})
	}
}
