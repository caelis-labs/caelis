package providers

import (
	"encoding/json"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

// Sparse cumulative deltas replace reported fields, including zero. Decode the
// extension fields too: MessageDeltaUsage does not type cache_creation's TTLs.
func mergeAnthropicUsage(current anthropic.Usage, delta string) anthropic.Usage {
	var previous, next map[string]json.RawMessage
	_ = json.Unmarshal([]byte(current.RawJSON()), &previous)
	_ = json.Unmarshal([]byte(delta), &next)
	if previous == nil {
		previous = map[string]json.RawMessage{}
	}
	for key, value := range next {
		if string(value) == "null" {
			continue
		}
		if key == "cache_creation" || key == "output_tokens_details" {
			var oldFields, newFields map[string]json.RawMessage
			_ = json.Unmarshal(previous[key], &oldFields)
			_ = json.Unmarshal(value, &newFields)
			if oldFields == nil {
				oldFields = map[string]json.RawMessage{}
			}
			for field, count := range newFields {
				if string(count) != "null" {
					oldFields[field] = count
				}
			}
			value, _ = json.Marshal(oldFields)
		}
		previous[key] = value
	}
	raw, _ := json.Marshal(previous)
	var merged anthropic.Usage
	_ = json.Unmarshal(raw, &merged)
	return merged
}
