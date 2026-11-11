package support

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Redactor replaces the values a bundle must never carry. It is deliberately conservative:
// exact vault values first, then the shapes of credentials in logs and config. A pattern that
// is wrong is a false redaction in a diagnostic, which is cheaper than a leaked key.
type Redactor struct {
	values   []string
	home     string
	patterns []redaction
	counts   map[string]int
}

type redaction struct {
	name string
	re   *regexp.Regexp
	repl string
}

func NewRedactor(values []string, home string) *Redactor {
	seen := map[string]bool{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		// A one-character secret would turn every matching letter in a log into a redaction.
		// The vault requires a real value; four is the shortest one worth carrying.
		if len(value) < 4 || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	sort.Slice(unique, func(i, j int) bool { return len(unique[i]) > len(unique[j]) })
	return &Redactor{
		values: unique,
		home:   strings.TrimRight(home, "/"),
		counts: map[string]int{},
		patterns: []redaction{
			{
				name: "authorization headers",
				re:   regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(bearer\s+)?[A-Za-z0-9._~+/=-]{8,}`),
				repl: `${1}[redacted]`,
			},
			{
				name: "token-shaped settings",
				re:   regexp.MustCompile(`(?i)((?:access[_-]?token|refresh[_-]?token|api[_-]?key|password|secret|token)\s*["']?\s*[:=]\s*["']?)([^"'\s,}]{4,})`),
				repl: `${1}[redacted]`,
			},
			{
				name: "setup codes",
				re:   regexp.MustCompile(`([?&]code=)[A-Za-z0-9._~-]{4,}`),
				repl: `${1}[redacted]`,
			},
			{
				name: "URL credentials",
				re:   regexp.MustCompile(`(https?://)[^/@\s]+@`),
				repl: `${1}[redacted]@`,
			},
			{
				name: "recognised token prefixes",
				re:   regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,})\b`),
				repl: `[redacted]`,
			},
			{
				name: "email addresses",
				re:   regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
				repl: `[email]`,
			},
		},
	}
}

// Text redacts one piece of text.
func (r *Redactor) Text(text string) string {
	for _, value := range r.values {
		if count := strings.Count(text, value); count > 0 {
			r.counts["secret values"] += count
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	if r.home != "" {
		if count := strings.Count(text, r.home); count > 0 {
			r.counts["home paths"] += count
			text = strings.ReplaceAll(text, r.home, "~")
		}
	}
	for _, pattern := range r.patterns {
		if count := len(pattern.re.FindAllString(text, -1)); count > 0 {
			r.counts[pattern.name] += count
			text = pattern.re.ReplaceAllString(text, pattern.repl)
		}
	}
	return text
}

// Value redacts every string in a JSON-shaped value. It is used before a record is marshaled,
// so a key is redacted even when it is not a field the collector knew about.
func (r *Redactor) Value(value any) any {
	switch typed := value.(type) {
	case string:
		return r.Text(typed)
	case []any:
		for index := range typed {
			typed[index] = r.Value(typed[index])
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = r.Value(item)
		}
		return typed
	default:
		return value
	}
}

// Notes is what the manifest and the preview say was replaced, never the values.
func (r *Redactor) Notes() []string {
	order := []string{
		"secret values",
		"authorization headers",
		"token-shaped settings",
		"setup codes",
		"URL credentials",
		"recognised token prefixes",
		"email addresses",
		"home paths",
	}
	var notes []string
	for _, name := range order {
		if count := r.counts[name]; count > 0 {
			notes = append(notes, fmt.Sprintf("%s: %d", name, count))
		}
	}
	return notes
}
