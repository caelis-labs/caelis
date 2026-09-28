package wirev1

import "encoding/json"

// Tool-call observations precede policy admission, so even rejected calls can
// contain arguments outside v1's numeric range. Omit that optional display input
// rather than break the feed or present rounded/stringified arguments. Canonical
// model arguments and execution inputs are untouched. Approval and callback
// payloads are not optional observations and retain strict number validation.
func observableToolInput(input any) any {
	raw, err := json.Marshal(input)
	if err == nil && ValidateJSONNumbers(raw) != nil {
		return nil
	}
	return input
}
