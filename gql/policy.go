package gql

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
)

// policyError is the common, provider-facing catalog error shape.
func policyError(class *protocol.Class, action, field string) error {
	name := "<unknown>"
	if class != nil {
		name = class.Name
	}
	return fmt.Errorf("授权拒绝 class=%s action=%s field=%s", name, action, field)
}

func operationInList(list []string, action string) bool {
	return slices.Contains(list, action)
}

func allowedColumn(list []string, field *protocol.Field) bool {
	if field == nil {
		return false
	}
	for _, candidate := range list {
		if candidate == "*" || candidate == field.Name || candidate == field.Column {
			return true
		}
	}
	return false
}

func policyRows(value any) ([]map[string]interface{}, error) {
	switch typed := value.(type) {
	case map[string]interface{}:
		return []map[string]interface{}{typed}, nil
	case []interface{}:
		rows := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			row, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("input row is not an object")
			}
			rows = append(rows, row)
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("input is not an object or list")
	}
}

func evaluatePredicates(class *protocol.Class, action string, filters, predicates []protocol.Predicate, values map[string]any, scope map[string]interface{}) error {
	for _, pred := range predicates {
		value, ok := values[pred.Column]
		if !ok {
			if action == protocol.OP_UPDATE && predicateIn(filters, pred) {
				continue
			}
			return policyError(class, action, pred.Column)
		}
		expected := pred.Value
		if pred.Context != "" {
			contextValue, exists := scope[pred.Context]
			if !exists {
				return fmt.Errorf("missing check context %s", pred.Context)
			}
			expected = contextValue
		}
		if !predicateMatches(pred.Operator, value, expected) {
			return fmt.Errorf("check failed for %s", pred.Column)
		}
	}
	return nil
}

func predicateMatches(operator string, actual, expected any) bool {
	switch operator {
	case "eq":
		return fmt.Sprint(actual) == fmt.Sprint(expected)
	case "ne", "neq":
		return fmt.Sprint(actual) != fmt.Sprint(expected)
	case "is-null":
		return actual == nil
	case "not-null", "is-not-null":
		return actual != nil
	case "in":
		list := reflect.ValueOf(expected)
		if list.IsValid() && (list.Kind() == reflect.Array || list.Kind() == reflect.Slice) {
			for index := 0; index < list.Len(); index++ {
				if fmt.Sprint(actual) == fmt.Sprint(list.Index(index).Interface()) {
					return true
				}
			}
		}
	case "lt", "lte", "gt", "gte":
		order := predicateOrder(actual, expected)
		return operator == "lt" && order < 0 || operator == "lte" && order <= 0 || operator == "gt" && order > 0 || operator == "gte" && order >= 0
	}
	return false
}

func predicateOrder(left, right any) int {
	a, aerr := strconv.ParseFloat(fmt.Sprint(left), 64)
	b, berr := strconv.ParseFloat(fmt.Sprint(right), 64)
	if aerr == nil && berr == nil {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	return strings.Compare(fmt.Sprint(left), fmt.Sprint(right))
}

func predicateIn(list []protocol.Predicate, want protocol.Predicate) bool {
	for _, candidate := range list {
		if candidate.Column == want.Column && candidate.Operator == want.Operator && candidate.Context == want.Context && fmt.Sprint(candidate.Value) == fmt.Sprint(want.Value) {
			return true
		}
	}
	return false
}

func walkResolvedAny(value any, fn func(string)) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for name, child := range typed {
			fn(name)
			walkResolvedAny(child, fn)
		}
	case []interface{}:
		for _, child := range typed {
			walkResolvedAny(child, fn)
		}
	}
}
