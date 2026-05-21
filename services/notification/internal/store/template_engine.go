// Template engine: simple {{variable}} substitution against a
// payload map. Unknown variables are rendered as the empty string so a
// missing payload field never leaves "{{loan_no}}" in a user-facing
// message — the worst case is "Loan  approved" instead of an error.
//
// Whitespace inside the braces is tolerated: {{ loan_no }} and
// {{loan_no}} both resolve to the same key.

package store

import (
	"fmt"
	"regexp"
	"strings"
)

var tplVarRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

func RenderTemplate(template string, payload map[string]any) string {
	if template == "" {
		return ""
	}
	return tplVarRe.ReplaceAllStringFunc(template, func(match string) string {
		groups := tplVarRe.FindStringSubmatch(match)
		if len(groups) < 2 {
			return ""
		}
		key := groups[1]
		v, ok := payload[key]
		if !ok || v == nil {
			return ""
		}
		return fmt.Sprint(v)
	})
}

// CollectPlaceholders returns the unique set of variable names used in
// the supplied template. Used by the admin UI to validate templates
// against the event's allowed_variables list.
func CollectPlaceholders(template string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range tplVarRe.FindAllStringSubmatch(template, -1) {
		if len(m) < 2 {
			continue
		}
		k := strings.TrimSpace(m[1])
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}
