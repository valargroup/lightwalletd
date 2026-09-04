// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package common

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeHexJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []byte
		wantErr bool
	}{
		{name: "empty", input: `""`, want: []byte{}},
		{name: "mixed case", input: `"00aBff"`, want: []byte{0x00, 0xab, 0xff}},
		{name: "surrounding whitespace", input: " \n\t\"01\" \r", want: []byte{0x01}},
		{name: "escaped hex", input: `"00\u0061b"`, want: []byte{0x00, 0xab}},
		{name: "not a string", input: `null`, wantErr: true},
		{name: "odd length", input: `"abc"`, wantErr: true},
		{name: "non hex", input: `"gg"`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeHexJSON(json.RawMessage(test.input))
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, test.wantErr)
			}
			if !bytes.Equal(got, test.want) {
				t.Fatalf("got %x, want %x", got, test.want)
			}
		})
	}
}

func BenchmarkDecodeHexJSON(b *testing.B) {
	result := json.RawMessage(`"` + strings.Repeat("ab", 1_000_000) + `"`)
	b.SetBytes(1_000_000)
	b.ReportAllocs()
	for range b.N {
		if _, err := DecodeHexJSON(result); err != nil {
			b.Fatal(err)
		}
	}
}
