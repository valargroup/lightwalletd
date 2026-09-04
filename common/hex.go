// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package common

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// DecodeHexJSON decodes a hexadecimal JSON string. Backend raw-data replies
// use unescaped ASCII hex, so the common path decodes directly from the JSON
// bytes instead of first allocating an equally large Go string. The fallback
// preserves support for valid JSON escape sequences.
func DecodeHexJSON(result json.RawMessage) ([]byte, error) {
	result = bytes.TrimSpace(result)
	if len(result) < 2 || result[0] != '"' || result[len(result)-1] != '"' {
		return nil, errors.New("expected a JSON hex string")
	}

	hexBytes := result[1 : len(result)-1]
	if bytes.IndexByte(hexBytes, '\\') < 0 {
		decoded := make([]byte, hex.DecodedLen(len(hexBytes)))
		n, err := hex.Decode(decoded, hexBytes)
		if err != nil {
			return nil, err
		}
		return decoded[:n], nil
	}

	var encoded string
	if err := json.Unmarshal(result, &encoded); err != nil {
		return nil, err
	}
	return hex.DecodeString(encoded)
}
