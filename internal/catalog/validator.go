package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// ValidateAttributes checks a caller-supplied attribute map against a
// category's definitions and returns the normalized value to persist.
//
// It is a pure function of (definitions, values) so it can be unit-tested
// without a database, and it is the single gate for both `services.attributes`
// on write and `bookings.attributes` at reservation time.
//
// Every failure is collected rather than short-circuited: a dynamic form
// should be able to mark all of its bad fields at once.
func ValidateAttributes(defs []Attribute, values map[string]any) (map[string]any, error) {
	byKey := make(map[string]Attribute, len(defs))
	for _, def := range defs {
		byKey[def.Key] = def
	}

	problems := map[string]string{}
	normalized := make(map[string]any, len(values))

	// Unknown keys are rejected rather than ignored: silently dropping a field
	// a client thought it set is how bookings end up missing the one detail
	// the provider needed.
	for key := range values {
		if _, known := byKey[key]; !known {
			problems[key] = "is not a valid attribute for this category"
		}
	}

	for _, def := range defs {
		raw, present := values[def.Key]
		if !present || raw == nil {
			if def.Required {
				problems[def.Key] = "is required"
			}
			continue
		}

		value, err := coerce(def, raw)
		if err != nil {
			problems[def.Key] = err.Error()
			continue
		}
		normalized[def.Key] = value
	}

	if len(problems) > 0 {
		return nil, httpx.Validation(summarize(problems)).WithDetails(problems)
	}
	return normalized, nil
}

// coerce checks one value against one definition and returns the canonical
// form to store.
func coerce(def Attribute, raw any) (any, error) {
	switch def.DataType {
	case TypeText:
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("must be text")
		}
		text = strings.TrimSpace(text)
		if text == "" && def.Required {
			return nil, fmt.Errorf("is required")
		}
		return text, nil

	case TypeNumber:
		number, err := toNumber(raw)
		if err != nil {
			return nil, err
		}
		return number, nil

	case TypeBool:
		flag, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("must be true or false")
		}
		return flag, nil

	case TypeEnum:
		choice, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("must be one of: %s", strings.Join(def.Options, ", "))
		}
		if !contains(def.Options, choice) {
			return nil, fmt.Errorf("must be one of: %s", strings.Join(def.Options, ", "))
		}
		return choice, nil

	case TypeMultiEnum:
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("must be a list of: %s", strings.Join(def.Options, ", "))
		}
		seen := make(map[string]struct{}, len(list))
		choices := make([]string, 0, len(list))
		for _, item := range list {
			choice, ok := item.(string)
			if !ok || !contains(def.Options, choice) {
				return nil, fmt.Errorf("must be a list of: %s", strings.Join(def.Options, ", "))
			}
			if _, duplicate := seen[choice]; duplicate {
				return nil, fmt.Errorf("must not repeat a value")
			}
			seen[choice] = struct{}{}
			choices = append(choices, choice)
		}
		if len(choices) == 0 && def.Required {
			return nil, fmt.Errorf("is required")
		}
		// Sorted so `@>` containment in discovery does not depend on the order
		// the client happened to send.
		sort.Strings(choices)
		return choices, nil

	default:
		// Unreachable while the CHECK constraint holds; treated as a data bug
		// rather than a caller error.
		return nil, fmt.Errorf("has an unsupported data type %q", def.DataType)
	}
}

func toNumber(raw any) (float64, error) {
	switch value := raw.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, fmt.Errorf("must be a finite number")
		}
		return value, nil
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, fmt.Errorf("must be a number")
		}
		return parsed, nil
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	default:
		return 0, fmt.Errorf("must be a number")
	}
}

func contains(options []string, value string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

// summarize turns the problem map into one human sentence for `message`, with
// the full per-field detail carried alongside in `details`.
func summarize(problems map[string]string) string {
	keys := make([]string, 0, len(problems))
	for key := range problems {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	if len(keys) == 1 {
		return fmt.Sprintf("%s %s", keys[0], problems[keys[0]])
	}
	return fmt.Sprintf("%d attributes are invalid: %s", len(keys), strings.Join(keys, ", "))
}
