package projector

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const projectionIDPrefix = "acp-projection:"

// formatProjectionID returns the stable identity of one projection of a
// durable Session event. It is an identity and must not be accepted as a
// public resume Cursor.
func formatProjectionID(eventID string, index int) string {
	return fmt.Sprintf("%s%s:%d", projectionIDPrefix, base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(eventID))), index)
}
