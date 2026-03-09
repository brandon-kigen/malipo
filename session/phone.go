package session

import "errors"

// normalizePhone converts a Kenyan phone number into the international
// E.164 format (+254XXXXXXXXX).
//
// The function accepts common Kenyan number representations including:
//
//	0712345678
//	01XXXXXXXX
//	712345678
//	+254712345678
//	254712345678
//	(0712) 345-678
//
// All valid inputs are normalized to:
//
//	+254XXXXXXXXX
//
// The function performs strict validation to ensure the number matches
// known Kenyan mobile prefixes (07 or 01) and contains the correct
// number of digits.
//
// Performance characteristics:
//
//   - Single pass input scan
//   - No regex usage
//   - No intermediate string allocations
//   - Only one allocation for the final normalized number
//
// The function returns an error if:
//
//   - The input contains invalid characters
//   - The number length is invalid
//   - The number does not match a Kenyan mobile prefix
func normalizePhone(p string) (string, error) {
	if len(p) == 0 {
		return "", errors.New("empty phone number")
	}

	var digits [12]byte
	n := 0

	for i := 0; i < len(p); i++ {
		c := p[i]

		switch {
		case c == '+' && n == 0:
			// leading plus consumed, nothing stored

		case c >= '0' && c <= '9':
			if n >= len(digits) {
				return "", errors.New("phone number too long")
			}
			digits[n] = c
			n++

		case c == ' ' || c == '-' || c == '(' || c == ')':
			continue

		default:
			return "", errors.New("invalid character in phone number")
		}
	}

	if n == 0 {
		return "", errors.New("empty phone number")
	}

	switch {
	// +254XXXXXXXXX or 254XXXXXXXXX
	case n == 12 &&
		digits[0] == '2' && digits[1] == '5' && digits[2] == '4' &&
		isValidKenyanPrefix(digits[3], digits[4]):

		out := make([]byte, 13)
		out[0] = '+'
		copy(out[1:], digits[:12])
		return string(out), nil

	// 07XXXXXXXX or 01XXXXXXXX
	case n == 10 &&
		digits[0] == '0' &&
		isValidKenyanPrefix(digits[1], digits[2]):

		out := make([]byte, 13)
		copy(out, "+254")
		copy(out[4:], digits[1:10])
		return string(out), nil

	// 7XXXXXXXX or 1XXXXXXXX
	case n == 9 &&
		isValidKenyanPrefix(digits[0], digits[1]):

		out := make([]byte, 13)
		copy(out, "+254")
		copy(out[4:], digits[:9])
		return string(out), nil
	}

	return "", errors.New("invalid Kenyan phone number")
}

// isValidKenyanPrefix verifies that the number starts with a valid
// Kenyan mobile prefix.
//
// Valid prefixes:
//
//	7X  → traditional Kenyan mobile numbers (07XXXXXXXX)
//	1X  → newer Kenyan mobile ranges (01XXXXXXXX)
//
// The second digit is currently allowed to be any numeric value.
func isValidKenyanPrefix(a, b byte) bool {
	switch a {
	case '7', '1':
		return b >= '0' && b <= '9'
	default:
		return false
	}
}
