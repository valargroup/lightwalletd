// Copyright (c) 2025 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package hash32

import (
	"bytes"
	"testing"
)

// FromSlice documents that short inputs are zero-padded and long inputs
// truncated; neither may panic (a plain T(arg) conversion panics on input
// shorter than 32 bytes).
func TestFromSlice(t *testing.T) {
	short := FromSlice([]byte{1, 2, 3})
	if short != (T{1, 2, 3}) {
		t.Error("short input not zero-padded:", short)
	}
	long := make([]byte, 33)
	for i := range long {
		long[i] = byte(i + 1)
	}
	got := FromSlice(long)
	if !bytes.Equal(ToSlice(got), long[:32]) {
		t.Error("long input not truncated to 32 bytes:", got)
	}
	if FromSlice(nil) != Nil {
		t.Error("nil input should produce the Nil hash")
	}
}

func TestReverseSliceShort(t *testing.T) {
	// Must not panic; the short input is zero-padded before reversing.
	got := ReverseSlice([]byte{1, 2, 3})
	want := make([]byte, 32)
	want[29], want[30], want[31] = 3, 2, 1
	if !bytes.Equal(got, want) {
		t.Error("unexpected reverse of short slice:", got)
	}
}
