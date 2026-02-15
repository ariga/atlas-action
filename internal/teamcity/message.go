// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package teamcity

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf16"
)

// Message represents a TeamCity service message.
type Message struct {
	Type  string
	Attrs MessageAttributes
}

// String encodes the Message to a string in the following format:
//
// ##teamcity[type key='value' key='value']
// https://www.jetbrains.com/help/teamcity/service-messages.html#Service+Messages+Formats
func (m Message) String() string {
	var b strings.Builder
	b.WriteString("##teamcity[")
	b.WriteString(m.Type)
	b.WriteString(m.Attrs.String())
	b.WriteString("]")
	return b.String()
}

// MessageAttributes is a named "map[string]string" type to hold key-value pairs
// passed to a TeamCity service message.
type MessageAttributes map[string]string

// Option is a function that modifies the MessageAttributes of a message.
type Option func(MessageAttributes)

// WithOptions applies the given options to the MessageAttributes
// and returns the modified attributes.
func (attrs MessageAttributes) WithOptions(opts []Option) MessageAttributes {
	for _, opt := range opts {
		opt(attrs)
	}
	return attrs
}

const (
	msgQuote     = '\''
	msgSeparator = ' '
)

// String encodes the MessageAttributes to a string as space-separated
// 'key=value' pairs. The pairs are joined in a sorted order by key.
func (attrs MessageAttributes) String() string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(attrs)) {
		b.WriteByte(msgSeparator)
		if k != "" {
			b.WriteString(escapeString(k))
			b.WriteByte('=')
		}
		b.WriteByte(msgQuote)
		b.WriteString(escapeString(attrs[k]))
		b.WriteByte(msgQuote)
	}
	return b.String()
}

// escapeString escapes a string according to TeamCity service message rules.
// https://www.jetbrains.com/help/teamcity/service-messages.html#Escaped+Values
func escapeString(val string) string {
	b := make([]byte, 0, len(val))
	for _, r := range val {
		switch {
		case r == '\n':
			b = append(b, '|', 'n')
		case r == '\r':
			b = append(b, '|', 'r')
		case r == '\u0085':
			b = append(b, '|', 'x')
		case r == '\u2028':
			b = append(b, '|', 'l')
		case r == '\u2029':
			b = append(b, '|', 'p')
		case r == '|', r == '[', r == ']', r == '\'':
			b = append(b, '|', byte(r))
		case r <= 127: // unicode.MaxASCII
			b = append(b, byte(r))
		case r <= 0xFFFF:
			// Characters in the Basic Multilingual Plane (BMP)
			b = fmt.Appendf(b, "|0x%04X", r)
		default:
			// Non-BMP characters (> 0xFFFF) need to be encoded as UTF-16 surrogate pairs
			// TeamCity expects two |0xXXXX sequences for these characters
			pair := utf16.Encode([]rune{r})
			for _, code := range pair {
				b = fmt.Appendf(b, "|0x%04X", code)
			}
		}
	}
	return string(b)
}
