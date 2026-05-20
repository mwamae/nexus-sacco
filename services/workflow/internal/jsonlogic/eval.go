// Tiny JSONLogic-subset evaluator for workflow-level conditions.
// Supports: ==, !=, <, <=, >, >=, &&, ||, !, var, in.
// Numeric comparison coerces strings via json.Number. Strings compare lexically.
//
// Examples:
//   {">": [ {"var": "amount"}, 500000 ]}
//   {"==": [ {"var": "kind"}, "loan" ]}
//   {"&&": [ {">": [...]}, {"==": [...]} ]}
//
// Trade-offs: this is deliberately minimal. We don't ship the full
// jsonlogic spec; if a workflow needs richer logic, the host module
// computes a derived field in `context` (eg `amount_band: "high"`)
// and conditions stay simple.

package jsonlogic

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Eval evaluates expr against data (a map of vars). Returns the truthiness
// of the result. nil expr or empty object → always true.
func Eval(expr any, data map[string]any) (bool, error) {
	v, err := evalAny(expr, data)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

func evalAny(expr any, data map[string]any) (any, error) {
	switch e := expr.(type) {
	case nil:
		return true, nil
	case bool, json.Number, float64, int, int64, string:
		return e, nil
	case []any:
		// Bare arrays are values (lists of literals).
		return e, nil
	case map[string]any:
		if len(e) == 0 {
			return true, nil
		}
		// Take the first (and only) operator.
		var op string
		var args any
		for k, v := range e {
			op = k
			args = v
			break
		}
		return applyOp(op, args, data)
	}
	return nil, fmt.Errorf("jsonlogic: unsupported expression type %T", expr)
}

func applyOp(op string, args any, data map[string]any) (any, error) {
	argList, _ := args.([]any)
	// "var" takes a single string argument
	if op == "var" {
		key := ""
		switch v := args.(type) {
		case string:
			key = v
		case []any:
			if len(v) > 0 {
				if s, ok := v[0].(string); ok {
					key = s
				}
			}
		}
		return lookupVar(key, data), nil
	}
	switch op {
	case "&&", "and":
		for _, a := range argList {
			v, err := evalAny(a, data)
			if err != nil {
				return nil, err
			}
			if !truthy(v) {
				return false, nil
			}
		}
		return true, nil
	case "||", "or":
		for _, a := range argList {
			v, err := evalAny(a, data)
			if err != nil {
				return nil, err
			}
			if truthy(v) {
				return true, nil
			}
		}
		return false, nil
	case "!":
		if len(argList) == 0 {
			return true, nil
		}
		v, err := evalAny(argList[0], data)
		if err != nil {
			return nil, err
		}
		return !truthy(v), nil
	case "in":
		if len(argList) != 2 {
			return false, fmt.Errorf("in: expected 2 args")
		}
		needle, err := evalAny(argList[0], data)
		if err != nil {
			return nil, err
		}
		hay, err := evalAny(argList[1], data)
		if err != nil {
			return nil, err
		}
		if arr, ok := hay.([]any); ok {
			for _, e := range arr {
				if equal(e, needle) {
					return true, nil
				}
			}
			return false, nil
		}
		if s, ok := hay.(string); ok {
			if sn, ok := needle.(string); ok {
				return strings.Contains(s, sn), nil
			}
		}
		return false, nil
	}

	// Binary ops require exactly two args.
	if len(argList) != 2 {
		return nil, fmt.Errorf("%s: expected 2 args, got %d", op, len(argList))
	}
	a, err := evalAny(argList[0], data)
	if err != nil {
		return nil, err
	}
	b, err := evalAny(argList[1], data)
	if err != nil {
		return nil, err
	}

	switch op {
	case "==":
		return equal(a, b), nil
	case "!=":
		return !equal(a, b), nil
	case "<", "<=", ">", ">=":
		return compare(a, b, op)
	}
	return nil, fmt.Errorf("jsonlogic: unsupported operator %q", op)
}

func lookupVar(key string, data map[string]any) any {
	if key == "" {
		return data
	}
	parts := strings.Split(key, ".")
	var cur any = data
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// truthy mirrors JSONLogic semantics.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func equal(a, b any) bool {
	// Try numeric equality first.
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func compare(a, b any, op string) (bool, error) {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		switch op {
		case "<":
			return af < bf, nil
		case "<=":
			return af <= bf, nil
		case ">":
			return af > bf, nil
		case ">=":
			return af >= bf, nil
		}
	}
	as := fmt.Sprintf("%v", a)
	bs := fmt.Sprintf("%v", b)
	switch op {
	case "<":
		return as < bs, nil
	case "<=":
		return as <= bs, nil
	case ">":
		return as > bs, nil
	case ">=":
		return as >= bs, nil
	}
	return false, fmt.Errorf("compare: unsupported op %q", op)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}
