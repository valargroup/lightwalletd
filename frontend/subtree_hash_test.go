package frontend

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/zcash/lightwalletd/common"
	"github.com/zcash/lightwalletd/hash32"
	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type subtreeCapture struct {
	testgetsubtreeroots
	rows []*walletrpc.SubtreeRoot
}

func (s *subtreeCapture) Send(row *walletrpc.SubtreeRoot) error {
	s.rows = append(s.rows, row)
	return nil
}

func TestSubtreeCompletingHashCacheAndRPC(t *testing.T) {
	defer resetGlobals()
	display, _ := hex.DecodeString(testBlockid)
	for _, mode := range []string{"no-cache", "cache-miss", "cache-hit"} {
		t.Run(mode, func(t *testing.T) {
			s := &lwdStreamer{}
			if mode != "no-cache" {
				s.cache = common.NewBlockCache(t.TempDir(), "unittestnet", 380640, -1)
				defer s.cache.Close()
				if mode == "cache-hit" {
					if err := s.cache.Add(380640, &walletrpc.CompactBlock{
						Height: 380640, Hash: hash32.ReverseSlice(display), PrevHash: make([]byte, 32),
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			hashCalls := 0
			common.RawRequest = func(ctx context.Context, method string, params []json.RawMessage) (json.RawMessage, error) {
				switch method {
				case "z_getsubtreesbyindex":
					return json.RawMessage(`{"subtrees":[{"root":"` + testTxid + `","end_height":380640}]}`), nil
				case "getblockhash":
					hashCalls++
					if mode == "cache-hit" || len(params) != 1 || string(params[0]) != "380640" {
						t.Fatalf("unexpected hash lookup: %s %s", mode, params)
					}
					return json.Marshal(testBlockid)
				default:
					t.Fatalf("unnecessary backend call: %s", method)
					return nil, nil
				}
			}
			stream := &subtreeCapture{}
			if err := s.GetSubtreeRoots(&walletrpc.GetSubtreeRootsArg{ShieldedProtocol: walletrpc.ShieldedProtocol_orchard}, stream); err != nil {
				t.Fatal(err)
			}
			if len(stream.rows) != 1 || stream.rows[0].CompletingBlockHeight != 380640 || !bytes.Equal(stream.rows[0].CompletingBlockHash, display) {
				t.Fatalf("incorrect subtree response: %v", stream.rows)
			}
			if mode != "cache-hit" && hashCalls != 1 {
				t.Fatalf("got %d hash calls", hashCalls)
			}
		})
	}
}

func TestSubtreeBlockHashInvalidBackendResponses(t *testing.T) {
	defer resetGlobals()
	for _, reply := range []string{`null`, `{}`, `"xyz"`, `"00"`, `"000000000000000000000000000000000000000000000000000000000000000000"`, `invalid`} {
		t.Run(reply, func(t *testing.T) {
			common.RawRequest = func(context.Context, string, []json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(reply), nil
			}
			if _, err := (&lwdStreamer{}).subtreeBlockHash(context.Background(), 1); status.Code(err) != codes.Internal {
				t.Fatalf("expected Internal, got %v", err)
			}
		})
	}
	common.RawRequest = func(context.Context, string, []json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("block unavailable")
	}
	if _, err := (&lwdStreamer{}).subtreeBlockHash(context.Background(), 1); status.Code(err) != codes.Internal {
		t.Fatalf("expected backend error, got %v", err)
	}
}

func TestSubtreeBlockHashCancellationAndNegativeHeight(t *testing.T) {
	defer resetGlobals()
	common.RawRequest = func(context.Context, string, []json.RawMessage) (json.RawMessage, error) {
		t.Fatal("invalid or canceled request reached backend")
		return nil, nil
	}
	s := &lwdStreamer{}
	if _, err := s.subtreeBlockHash(context.Background(), -1); status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.subtreeBlockHash(ctx, 1); status.Code(err) != codes.Canceled {
		t.Fatalf("expected Canceled, got %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	common.RawRequest = func(received context.Context, _ string, _ []json.RawMessage) (json.RawMessage, error) {
		if received != ctx {
			t.Fatal("request context was not forwarded")
		}
		cancel()
		return nil, ctx.Err()
	}
	if _, err := s.subtreeBlockHash(ctx, 1); status.Code(err) != codes.Canceled {
		t.Fatalf("expected Canceled during RPC, got %v", err)
	}
}
