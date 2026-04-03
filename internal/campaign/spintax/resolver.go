package spintax

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
)

var (
	// Matches innermost brace groups: {opt1|opt2|opt3} with no nested braces.
	reSpintax = regexp.MustCompile(`\{([^{}]+)\}`)
	// Matches template variable placeholders: {{name}}.
	reVariable = regexp.MustCompile(`\{\{(\w+)\}\}`)
)

// Resolve resolves all spintax in the input string by randomly selecting
// one alternative from each {opt1|opt2|...} group. Handles nested spintax
// by resolving innermost groups first. Preserves {{variable}} placeholders.
func Resolve(input string) string {
	// Protect {{var}} placeholders by replacing each with a unique token.
	var placeholders []string
	input = reVariable.ReplaceAllStringFunc(input, func(match string) string {
		idx := len(placeholders)
		placeholders = append(placeholders, match)
		return fmt.Sprintf("\x00%d\x00", idx)
	})

	// Resolve spintax from innermost groups outward.
	for reSpintax.MatchString(input) {
		input = reSpintax.ReplaceAllStringFunc(input, func(match string) string {
			inner := match[1 : len(match)-1]
			options := strings.Split(inner, "|")
			return options[rand.Intn(len(options))]
		})
	}

	// Restore {{var}} placeholders.
	for i, ph := range placeholders {
		input = strings.Replace(input, fmt.Sprintf("\x00%d\x00", i), ph, 1)
	}
	return input
}

// SubstituteVariables replaces {{key}} placeholders with values from the vars map.
func SubstituteVariables(input string, vars map[string]string) string {
	for k, v := range vars {
		input = strings.ReplaceAll(input, "{{"+k+"}}", v)
	}
	return input
}

// ExtractVariables returns the unique variable names found in a template body.
// Variables are identified by the {{name}} pattern.
func ExtractVariables(body string) []string {
	matches := reVariable.FindAllStringSubmatch(body, -1)
	seen := make(map[string]bool)
	var vars []string
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			vars = append(vars, m[1])
		}
	}
	return vars
}
