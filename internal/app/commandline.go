package app

import (
	"fmt"
	"strings"
)

func splitCommandLine(input string) ([]string, error) {
	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	seen := false

	flush := func() {
		if seen {
			args = append(args, current.String())
			current.Reset()
			seen = false
		}
	}

	for _, r := range input {
		switch {
		case escaped:
			current.WriteRune(r)
			seen = true
			escaped = false
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
				seen = true
			}
		case r == '\'' || r == '"':
			quote = r
			seen = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			current.WriteRune(r)
			seen = true
		}
	}

	if escaped {
		return nil, fmt.Errorf("dangling escape")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	return args, nil
}
