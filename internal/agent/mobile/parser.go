package mobile

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseJSON unmarshals an LLM response into the target struct, stripping
// markdown code fences if present.
func parseJSON(raw string, target any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty response")
	}

	if strings.HasPrefix(raw, "```json") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	} else if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	}

	if err := json.Unmarshal([]byte(raw), target); err != nil {
		repaired := strings.ReplaceAll(raw, ",}", "}")
		repaired = strings.ReplaceAll(repaired, ",]", "]")
		return json.Unmarshal([]byte(repaired), target)
	}
	return nil
}