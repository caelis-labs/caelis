package application

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// schemaValue builds a validation-only numeric view. jsonschema-go's type check
// treats json.Number as a string, although its constraint helpers accept it.
// Use native integers where possible and losslessly round-trippable floats
// otherwise; never replace the authoritative receipt's original JSON numbers.
func schemaValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		if number, err := value.Int64(); err == nil {
			return number, nil
		}
		if number, err := strconv.ParseUint(string(value), 10, 64); err == nil {
			return number, nil
		}
		number, err := value.Float64()
		if err != nil {
			return nil, ErrInvalid
		}
		if number == 0 {
			// Reject underflow without allocating an arbitrary-size exponent.
			mantissa := strings.FieldsFunc(string(value), func(r rune) bool { return r == 'e' || r == 'E' })[0]
			if strings.ContainsAny(mantissa, "123456789") {
				return nil, ErrInvalid
			}
			return number, nil
		}
		original, ok := new(big.Rat).SetString(string(value))
		roundTrip, roundTripOK := new(big.Rat).SetString(strconv.FormatFloat(number, 'g', -1, 64))
		if !ok || !roundTripOK || original.Cmp(roundTrip) != 0 {
			return nil, ErrInvalid
		}
		return number, nil
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			converted, err := schemaValue(child)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			converted, err := schemaValue(child)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	default:
		return value, nil
	}
}
