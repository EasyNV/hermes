package spintax

import (
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		allowed []string // any of these are valid outputs
	}{
		{
			name:    "no spintax",
			input:   "Hello world",
			allowed: []string{"Hello world"},
		},
		{
			name:    "single group",
			input:   "{Hi|Hello|Hey} there",
			allowed: []string{"Hi there", "Hello there", "Hey there"},
		},
		{
			name:    "multiple groups",
			input:   "{Hi|Hey} {world|there}",
			allowed: []string{"Hi world", "Hi there", "Hey world", "Hey there"},
		},
		{
			name:    "nested spintax",
			input:   "{Hi|{Hey|Yo}} there",
			allowed: []string{"Hi there", "Hey there", "Yo there"},
		},
		{
			name:    "single option",
			input:   "{Hello} world",
			allowed: []string{"Hello world"},
		},
		{
			name:    "empty input",
			input:   "",
			allowed: []string{""},
		},
		{
			name:    "preserves variables",
			input:   "{Hi|Hello} {{name}}",
			allowed: []string{"Hi {{name}}", "Hello {{name}}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run multiple times to exercise randomness.
			for i := 0; i < 20; i++ {
				result := Resolve(tt.input)
				found := false
				for _, a := range tt.allowed {
					if result == a {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Resolve(%q) = %q, not in allowed %v", tt.input, result, tt.allowed)
				}
			}
		})
	}
}

func TestResolve_Randomness(t *testing.T) {
	input := "{A|B|C}"
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		seen[Resolve(input)] = true
	}
	if len(seen) < 2 {
		t.Errorf("expected at least 2 distinct outputs from %q, got %d", input, len(seen))
	}
}

func TestSubstituteVariables(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		vars   map[string]string
		expect string
	}{
		{
			name:   "single variable",
			input:  "Hello {{name}}!",
			vars:   map[string]string{"name": "John"},
			expect: "Hello John!",
		},
		{
			name:   "multiple variables",
			input:  "Hi {{name}}, welcome to {{company}}",
			vars:   map[string]string{"name": "Jane", "company": "Acme"},
			expect: "Hi Jane, welcome to Acme",
		},
		{
			name:   "no variables",
			input:  "Hello world",
			vars:   map[string]string{"name": "John"},
			expect: "Hello world",
		},
		{
			name:   "missing variable left as-is",
			input:  "Hello {{name}}, {{missing}}",
			vars:   map[string]string{"name": "John"},
			expect: "Hello John, {{missing}}",
		},
		{
			name:   "empty vars",
			input:  "Hello {{name}}",
			vars:   map[string]string{},
			expect: "Hello {{name}}",
		},
		{
			name:   "repeated variable",
			input:  "{{name}} meets {{name}}",
			vars:   map[string]string{"name": "Alice"},
			expect: "Alice meets Alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SubstituteVariables(tt.input, tt.vars)
			if result != tt.expect {
				t.Errorf("SubstituteVariables(%q, %v) = %q, want %q", tt.input, tt.vars, result, tt.expect)
			}
		})
	}
}

func TestExtractVariables(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		expect []string
	}{
		{
			name:   "single variable",
			body:   "Hello {{name}}",
			expect: []string{"name"},
		},
		{
			name:   "multiple variables",
			body:   "{{name}} at {{company}} in {{city}}",
			expect: []string{"name", "company", "city"},
		},
		{
			name:   "no variables",
			body:   "Hello world",
			expect: nil,
		},
		{
			name:   "duplicates deduplicated",
			body:   "{{name}} meets {{name}} at {{place}}",
			expect: []string{"name", "place"},
		},
		{
			name:   "mixed with spintax",
			body:   "{Hi|Hello} {{name}}, {check|see} {{link}}",
			expect: []string{"name", "link"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractVariables(tt.body)
			if len(result) != len(tt.expect) {
				t.Fatalf("ExtractVariables(%q) = %v, want %v", tt.body, result, tt.expect)
			}
			for i, v := range result {
				if v != tt.expect[i] {
					t.Errorf("ExtractVariables(%q)[%d] = %q, want %q", tt.body, i, v, tt.expect[i])
				}
			}
		})
	}
}

func TestResolveAndSubstitute_Integration(t *testing.T) {
	template := "{Hi|Hey|Hello} {{name}}, {check this out|take a look}"
	vars := map[string]string{"name": "John"}

	for i := 0; i < 20; i++ {
		resolved := Resolve(template)
		result := SubstituteVariables(resolved, vars)

		if strings.Contains(result, "{{") {
			t.Errorf("unresolved variable in %q", result)
		}
		if strings.Contains(result, "{") || strings.Contains(result, "}") {
			t.Errorf("unresolved spintax in %q", result)
		}
		if !strings.Contains(result, "John") {
			t.Errorf("variable not substituted in %q", result)
		}
	}
}
