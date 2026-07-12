package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func workerString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func workerMap(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}

func workerStringMap(value any) (map[string]string, error) {
	if value == nil {
		return map[string]string{}, nil
	}
	source, ok := workerMap(value)
	if !ok {
		return nil, fmt.Errorf("environment must be an object")
	}
	result := make(map[string]string, len(source))
	for key, item := range source {
		if strings.ContainsAny(key, "=\x00") || key == "" {
			return nil, fmt.Errorf("invalid environment variable name")
		}
		result[key] = workerString(item)
	}
	return result, nil
}

func workerStringSlice(value any) ([]string, error) {
	switch selected := value.(type) {
	case []string:
		return append([]string(nil), selected...), nil
	case []any:
		result := make([]string, 0, len(selected))
		for _, item := range selected {
			result = append(result, workerString(item))
		}
		return result, nil
	case string:
		if strings.TrimSpace(selected) == "" {
			return nil, nil
		}
		return []string{selected}, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("command must be a string or array")
	}
}

func workerInt(value any, fallback int, minimum int, maximum int) int {
	selected := fallback
	switch number := value.(type) {
	case float64:
		selected = int(number)
	case int:
		selected = number
	case json.Number:
		if parsed, err := strconv.Atoi(number.String()); err == nil {
			selected = parsed
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(number)); err == nil {
			selected = parsed
		}
	}
	if selected < minimum {
		return minimum
	}
	if selected > maximum {
		return maximum
	}
	return selected
}

func workerBool(value any) bool {
	selected, _ := value.(bool)
	return selected
}

type workerTaskError struct {
	message   string
	retryable bool
}

func (e workerTaskError) Error() string   { return e.message }
func (e workerTaskError) Retryable() bool { return e.retryable }

func permanentWorkerError(format string, arguments ...any) error {
	return workerTaskError{message: fmt.Sprintf(format, arguments...)}
}

func retryableWorkerError(format string, arguments ...any) error {
	return workerTaskError{message: fmt.Sprintf(format, arguments...), retryable: true}
}
