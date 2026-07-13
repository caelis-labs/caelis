package eventstream

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const projectionIDPrefix = "acp-projection:"

// FormatProjectionID returns the stable identity of one projection of a
// durable Session event. It is an identity and must not be accepted as a
// public resume Cursor.
func FormatProjectionID(eventID string, index int) string {
	return fmt.Sprintf("%s%s:%d", projectionIDPrefix, base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(eventID))), index)
}
