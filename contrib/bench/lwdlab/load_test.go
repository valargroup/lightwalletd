// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package main

import (
	"context"
	"net"
	"sync"
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

type scanServer struct {
	walletrpc.UnimplementedCompactTxStreamerServer
	mu     sync.Mutex
	ranges [][2]uint64
	fault  string
}

func (s *scanServer) GetLightdInfo(context.Context, *walletrpc.Empty) (*walletrpc.LightdInfo, error) {
	chain := "main"
	if s.fault == "wrong-network" {
		chain = "regtest"
	}
	return &walletrpc.LightdInfo{ChainName: chain}, nil
}

func (s *scanServer) GetLatestBlock(context.Context, *walletrpc.ChainSpec) (*walletrpc.BlockID, error) {
	return &walletrpc.BlockID{Height: 3_500_000}, nil
}

func (s *scanServer) GetBlock(_ context.Context, block *walletrpc.BlockID) (*walletrpc.CompactBlock, error) {
	return &walletrpc.CompactBlock{Height: block.Height, Hash: make([]byte, 32)}, nil
}

func (s *scanServer) GetBlockRange(r *walletrpc.BlockRange, stream walletrpc.CompactTxStreamer_GetBlockRangeServer) error {
	s.mu.Lock()
	s.ranges = append(s.ranges, [2]uint64{r.Start.Height, r.End.Height})
	s.mu.Unlock()
	for height := r.Start.Height; height <= r.End.Height; height++ {
		if s.fault == "short" && height == r.End.Height {
			return nil
		}
		if s.fault == "wrong-height" {
			height++
		}
		if err := stream.Send(&walletrpc.CompactBlock{Height: height}); err != nil {
			return err
		}
	}
	return nil
}

func TestRangeScan(t *testing.T) {
	for _, fault := range []string{"", "short", "wrong-height", "wrong-network"} {
		t.Run(fault, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			fixture := &scanServer{fault: fault}
			walletrpc.RegisterCompactTxStreamerServer(server, fixture)
			go server.Serve(listener)
			defer server.Stop()
			result, err := executeLoad(loadConfig{
				address: listener.Addr().String(), operation: "range", concurrency: 1,
				startHeight: 3_000_000, endHeight: 3_002_049, rangeBatchSize: 1000,
				scanTimeout: time.Second, requireMainnet: true,
			})
			if fault == "wrong-network" {
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				if err == nil || len(fixture.ranges) != 0 {
					t.Fatalf("wrong network was not rejected before load: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			scan := result["range_scan"].(map[string]any)
			if fault != "" {
				if result["errors"] != uint64(1) || scan["complete"] != false || len(fixture.ranges) != 1 {
					t.Fatalf("failed stream was not stopped and counted: %v, ranges=%v", result, fixture.ranges)
				}
				return
			}
			want := [][2]uint64{{3_000_000, 3_000_999}, {3_001_000, 3_001_999}, {3_002_000, 3_002_049}}
			if len(fixture.ranges) != len(want) {
				t.Fatalf("ranges=%v", fixture.ranges)
			}
			for i := range want {
				if fixture.ranges[i] != want[i] {
					t.Fatalf("ranges=%v", fixture.ranges)
				}
			}
			if result["messages"] != uint64(2050) || result["requests"] != uint64(3) || result["errors"] != uint64(0) || scan["complete"] != true {
				t.Fatalf("unexpected scan totals: %v", result)
			}
		})
	}
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
