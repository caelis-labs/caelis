// Package jsonvalue owns the cross-language numeric limits shared by Control
// admission and its public JSON transport.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
)

// MaxSafeInteger is the largest integer exactly supported by JavaScript numbers.
const MaxSafeInteger = uint64(1<<53 - 1)

// ValidateNumbers rejects numeric tokens outside the Control v1 range, including
// nested values and overwritten object keys, without rounding decimal or exponent
// notation through float64.
func ValidateNumbers(raw []byte) error {
	if !json.Valid(raw) {
		return fmt.Errorf("control wire v1: invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("control wire v1: decode JSON: %w", err)
		}
		if number, ok := token.(json.Number); ok {
			if err := validateNumber(number); err != nil {
				return err
			}
		}
	}
}

func validateNumber(value json.Number) error {
	text := value.String()
	approximate, err := strconv.ParseFloat(text, 64)
	magnitude := math.Abs(approximate)
	if math.IsInf(approximate, 0) || math.IsNaN(approximate) || err != nil && magnitude != 0 {
		return fmt.Errorf("control wire v1: JSON number %q exceeds the exact JavaScript range; encode it as a string", value)
	}
	maximum := float64(MaxSafeInteger)
	if magnitude < maximum {
		return nil
	}
	if magnitude > maximum || len(text) > 128 {
		return fmt.Errorf("control wire v1: JSON number %q exceeds the exact JavaScript range; encode it as a string", value)
	}
	number, ok := new(big.Rat).SetString(text)
	if !ok || new(big.Rat).Abs(number).Cmp(big.NewRat(int64(MaxSafeInteger), 1)) > 0 {
		return fmt.Errorf("control wire v1: JSON number %q exceeds the exact JavaScript range; encode it as a string", value)
	}
	return nil
}
