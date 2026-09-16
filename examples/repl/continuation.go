package main

import (
	"errors"
	"github.com/asalimonov/montygo/monterr"
	"strings"
)

// mode mirrors upstream ReplContinuationMode.
type mode int

const (
	complete mode = iota
	incompleteImplicit
	incompleteBlock
)

const decoratorWithoutDefinition = "Expected class, function definition or async function definition after decorator"

// continuationMode classifies the result of feeding source the way upstream
// detect_repl_continuation_mode classifies a parse. A parse error runs nothing,
// so an incomplete snippet can be fed again once more input arrives.
func continuationMode(source string, err error) mode {
	var syntax *monterr.SyntaxError
	if !errors.As(err, &syntax) {
		return complete
	}
	switch msg := syntax.Message; {
	case strings.HasPrefix(msg, "Expected an indented block after "):
		return incompleteBlock
	case msg == decoratorWithoutDefinition && lastLineIsDecorator(source):
		return incompleteImplicit
	case msg == "unexpected EOF while parsing",
		msg == "f-string: unterminated triple-quoted string",
		msg == "t-string: unterminated triple-quoted string":
		return incompleteImplicit
	case msg == "missing closing quote in string literal" && unterminatedTripleQuote(source):
		return incompleteImplicit
	}
	return complete
}

func lastLineIsDecorator(source string) bool {
	lines := strings.Split(strings.TrimRight(source, " \t\r\n"), "\n")
	return strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "@")
}

// unterminatedTripleQuote reports whether the first unterminated string
// literal in source opened with triple quotes.
func unterminatedTripleQuote(source string) bool {
	for i := 0; i < len(source); {
		switch c := source[i]; c {
		case '#':
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case '\'', '"':
			triple := strings.Repeat(string(c), 3)
			if strings.HasPrefix(source[i:], triple) {
				end := closeTriple(source, i+3, triple)
				if end < 0 {
					return true
				}
				i = end
				continue
			}
			end := closeSingle(source, i+1, c)
			if end < 0 {
				return false
			}
			i = end
		default:
			i++
		}
	}
	return false
}

func closeTriple(source string, i int, triple string) int {
	for i < len(source) {
		switch {
		case source[i] == '\\':
			i += 2
		case strings.HasPrefix(source[i:], triple):
			return i + 3
		default:
			i++
		}
	}
	return -1
}

func closeSingle(source string, i int, quote byte) int {
	for i < len(source) {
		switch source[i] {
		case '\\':
			i += 2
		case quote:
			return i + 1
		case '\n':
			return -1
		default:
			i++
		}
	}
	return -1
}
