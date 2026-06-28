package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

type UnixMillis int64

func (value *UnixMillis) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		*value = 0
		return nil
	}

	var text string
	if len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
	} else {
		text = string(trimmed)
	}

	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid unix milliseconds %q: %w", text, err)
	}
	*value = UnixMillis(parsed)
	return nil
}

func (value UnixMillis) Int64() int64 {
	return int64(value)
}
