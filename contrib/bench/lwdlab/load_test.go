// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type delayedTipServer struct {
	walletrpc.UnimplementedCompactTxStreamerServer
	err error
}

func (s *delayedTipServer) GetLatestBlock(ctx context.Context, _ *walletrpc.ChainSpec) (*walletrpc.BlockID, error) {
	select {
	case <-time.After(50 * time.Millisecond):
		return &walletrpc.BlockID{Height: 1}, s.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestLoadDrainsAndCountsErrors(t *testing.T) {
	for _, responseErr := range []error{nil, status.Error(codes.DeadlineExceeded, "backend timeout")} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := grpc.NewServer()
		walletrpc.RegisterCompactTxStreamerServer(server, &delayedTipServer{err: responseErr})
		go server.Serve(listener)
		result, err := executeLoad(loadConfig{address: listener.Addr().String(), operation: "latest-block", concurrency: 1, duration: 20 * time.Millisecond})
		server.Stop()
		if err != nil {
			t.Fatal(err)
		}
		requests, errors := uint64(1), uint64(0)
		if responseErr != nil {
			requests, errors = 0, 1
		}
		if result["requests"] != requests || result["errors"] != errors {
			t.Fatalf("unexpected counts: %v", result)
		}
		if result["elapsed_seconds"].(float64) < 0.05 {
			t.Fatal("load did not drain the request")
		}
	}
}
