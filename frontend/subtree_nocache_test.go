// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zcash/lightwalletd/common"
	"github.com/zcash/lightwalletd/hash32"
	"github.com/zcash/lightwalletd/walletrpc"
)

func TestSubtreeMetadataWithoutDiskCache(t *testing.T) {
	testT = t
	defer resetGlobals()
	lwd, err := NewLwdStreamer(nil, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	common.RawRequest = func(ctx context.Context, method string, params []json.RawMessage) (json.RawMessage, error) {
		if method == "z_getsubtreesbyindex" {
			return json.Marshal(common.ZcashdRpcReplyGetsubtreebyindex{Subtrees: []common.Subtree{{Root: strings.Repeat("11", 32), End_height: 380640}}})
		}
		return getLatestBlockStub(ctx, method, params)
	}
	stream := &testgetsubtreeroots{}
	if err := lwd.GetSubtreeRoots(&walletrpc.GetSubtreeRootsArg{ShieldedProtocol: walletrpc.ShieldedProtocol_sapling}, stream); err != nil {
		t.Fatal(err)
	}
	if step != 2 || len(stream.roots) != 1 || stream.roots[0].CompletingBlockHeight != 380640 {
		t.Fatal("expected one root from backend block metadata")
	}
	// The same backend block must yield the same metadata with or without caching.
	step = 0
	block, err := common.GetBlock(context.Background(), nil, 380640)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stream.roots[0].CompletingBlockHash, hash32.ReverseSlice(block.Hash)) {
		t.Fatal("incorrect completing block hash")
	}
}
