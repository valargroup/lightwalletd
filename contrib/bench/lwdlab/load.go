// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

type loadConfig struct {
	address         string
	operation       string
	concurrency     int
	duration        time.Duration
	iterations      int
	startHeight     uint64
	endHeight       uint64
	rangeBatchSize  uint64
	scanTimeout     time.Duration
	requireMainnet  bool
	blockHeight     uint64
	pools           []walletrpc.PoolType
	subtreeCount    uint32
	subtreeProtocol walletrpc.ShieldedProtocol
	mempoolCount    int
	mempoolExclude  int
	mempoolTxIDs    [][]byte
}

type workerResult struct {
	requests     uint64
	messages     uint64
	bytes        uint64
	errors       uint64
	errorSample  []string
	latency      []time.Duration
	scanComplete bool
}

func runLoad(args []string) {
	flags := flag.NewFlagSet("load", flag.ExitOnError)
	address := flags.String("address", "127.0.0.1:19067", "lightwalletd gRPC address")
	operation := flags.String("op", "poll", "block, range, tree, latest-tree, latest-block, info, poll, wallet-poll, wallet-load, subtree, or mempool")
	concurrency := flags.Int("concurrency", 32, "parallel clients and gRPC connections")
	duration := flags.Duration("duration", 10*time.Second, "measurement duration")
	iterations := flags.Int("iterations", 0, "requests per worker; overrides duration when positive")
	startHeight := flags.Uint64("start", 100, "range start height")
	endHeight := flags.Uint64("end", 131, "range end height")
	rangeBatchSize := flags.Uint64("range-batch", 0, "when positive, download start..end once per client in consecutive batches; requires op=range and overrides duration")
	scanTimeout := flags.Duration("scan-timeout", 30*time.Minute, "overall timeout for a finite range scan")
	requireMainnet := flags.Bool("require-mainnet", false, "verify server network and range end block before loading")
	blockHeight := flags.Uint64("height", 380640, "block or tree-state height")
	poolNames := flags.String("pools", "", "comma-separated transparent,sapling,orchard,ironwood")
	subtreePool := flags.String("subtree-pool", "sapling", "sapling, orchard, or ironwood")
	subtreeCount := flags.Uint("subtrees", 64, "maximum subtree roots")
	mempoolCount := flags.Int("mempool", 4000, "backend mempool size")
	mempoolExclude := flags.Int("exclude", 3900, "full txids to exclude from mempool response")
	_ = flags.Parse(args)
	if *concurrency < 1 || (*rangeBatchSize == 0 && *iterations <= 0 && *duration <= 0) {
		flags.Usage()
		os.Exit(2)
	}
	pools, err := parsePools(*poolNames)
	if err != nil {
		fatalf("parse pools: %v", err)
	}
	protocol, ok := walletrpc.ShieldedProtocol_value[*subtreePool]
	if !ok || protocol < 0 || protocol > 2 {
		fatalf("unknown subtree pool %q", *subtreePool)
	}
	config := loadConfig{
		address:         *address,
		operation:       *operation,
		concurrency:     *concurrency,
		duration:        *duration,
		iterations:      *iterations,
		startHeight:     *startHeight,
		endHeight:       *endHeight,
		rangeBatchSize:  *rangeBatchSize,
		scanTimeout:     *scanTimeout,
		requireMainnet:  *requireMainnet,
		blockHeight:     *blockHeight,
		pools:           pools,
		subtreeCount:    uint32(*subtreeCount),
		subtreeProtocol: walletrpc.ShieldedProtocol(protocol),
		mempoolCount:    *mempoolCount,
		mempoolExclude:  min(*mempoolExclude, *mempoolCount),
	}
	config.mempoolTxIDs = make([][]byte, config.mempoolExclude)
	for i := range config.mempoolTxIDs {
		config.mempoolTxIDs[i] = protocolTxID(i)
	}
	result, err := executeLoad(config)
	if err != nil {
		fatalf("load: %v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatalf("encode result: %v", err)
	}
}

func executeLoad(config loadConfig) (map[string]any, error) {
	if config.rangeBatchSize > 0 {
		if config.operation != "range" || config.startHeight > config.endHeight || config.endHeight > math.MaxUint32 || config.iterations != 0 || config.scanTimeout <= 0 {
			return nil, fmt.Errorf("range-batch requires op=range, ordered uint32 heights, no iterations, and a positive scan-timeout")
		}
		config.iterations = int(1 + (config.endHeight-config.startHeight)/config.rangeBatchSize)
	}
	connections := make([]*grpc.ClientConn, config.concurrency)
	clients := make([]walletrpc.CompactTxStreamerClient, config.concurrency)
	for i := range config.concurrency {
		dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		connection, err := grpc.DialContext(
			dialCtx,
			config.address,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		cancel()
		if err != nil {
			for j := 0; j < i; j++ {
				_ = connections[j].Close()
			}
			return nil, err
		}
		connections[i] = connection
		clients[i] = walletrpc.NewCompactTxStreamerClient(connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()

	var mainnetState map[string]any
	if config.requireMainnet {
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer checkCancel()
		info, err := clients[0].GetLightdInfo(checkCtx, &walletrpc.Empty{})
		if err != nil {
			return nil, fmt.Errorf("mainnet preflight info: %w", err)
		}
		if info.ChainName != "main" {
			return nil, fmt.Errorf("expected mainnet, server reports %q", info.ChainName)
		}
		tip, err := clients[0].GetLatestBlock(checkCtx, &walletrpc.ChainSpec{})
		if err != nil {
			return nil, fmt.Errorf("mainnet preflight tip: %w", err)
		}
		if tip.Height < config.endHeight {
			return nil, fmt.Errorf("mainnet tip %d is below range end %d", tip.Height, config.endHeight)
		}
		block, err := clients[0].GetBlock(checkCtx, &walletrpc.BlockID{Height: config.endHeight})
		if err != nil {
			return nil, fmt.Errorf("mainnet preflight end block: %w", err)
		}
		if block.Height != config.endHeight || len(block.Hash) != 32 {
			return nil, fmt.Errorf("mainnet preflight returned an invalid end block")
		}
		mainnetState = map[string]any{
			"server_info":             info,
			"observed_tip":            tip.Height,
			"range_end_height":        block.Height,
			"range_end_hash_wire_hex": hex.EncodeToString(block.Hash),
		}
	}

	ctx := context.Background()
	cancel := func() {}
	if config.iterations <= 0 {
		ctx, cancel = context.WithTimeout(ctx, config.duration+30*time.Second)
	} else if config.rangeBatchSize > 0 {
		ctx, cancel = context.WithTimeout(ctx, config.scanTimeout)
	}
	defer cancel()

	// Stop issuing work at the measurement deadline, then drain in-flight RPCs.
	// The extra 30 seconds bounds a stalled drain without treating deadline
	// cancellation as a successful or silently ignored request.
	var stopAt time.Time
	start := make(chan struct{})
	results := make(chan workerResult, config.concurrency)
	var wg sync.WaitGroup
	for worker := range config.concurrency {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			local := workerResult{latency: make([]time.Duration, 0, 8192)}
			<-start
			for iteration := 0; config.iterations <= 0 || iteration < config.iterations; iteration++ {
				if ctx.Err() != nil || (config.iterations <= 0 && !time.Now().Before(stopAt)) {
					break
				}
				began := time.Now()
				requestConfig := config
				if config.rangeBatchSize > 0 {
					requestConfig = scanRequestConfig(config, iteration)
				}
				messages, bytes, err := executeRequest(ctx, clients[worker], requestConfig, worker+iteration)
				elapsed := time.Since(began)
				if err != nil {
					local.errors++
					if len(local.errorSample) < 3 {
						local.errorSample = append(local.errorSample, err.Error())
					}
					if config.rangeBatchSize > 0 {
						break
					}
					continue
				}
				local.requests++
				local.messages += messages
				local.bytes += bytes
				if len(local.latency) < 200_000 {
					local.latency = append(local.latency, elapsed)
				}
			}
			local.scanComplete = config.rangeBatchSize > 0 && local.requests == uint64(config.iterations)
			results <- local
		}(worker)
	}
	started := time.Now()
	stopAt = started.Add(config.duration)
	close(start)
	wg.Wait()
	elapsed := time.Since(started)
	close(results)

	total := workerResult{}
	completedScans := 0
	for result := range results {
		if result.scanComplete {
			completedScans++
		}
		total.requests += result.requests
		total.messages += result.messages
		total.bytes += result.bytes
		total.errors += result.errors
		if len(total.errorSample) < 10 {
			remaining := 10 - len(total.errorSample)
			total.errorSample = append(total.errorSample, result.errorSample[:min(remaining, len(result.errorSample))]...)
		}
		total.latency = append(total.latency, result.latency...)
	}
	slices.Sort(total.latency)
	seconds := elapsed.Seconds()
	result := map[string]any{
		"operation":           config.operation,
		"concurrency":         config.concurrency,
		"elapsed_seconds":     seconds,
		"requests":            total.requests,
		"requests_per_second": float64(total.requests) / seconds,
		"messages":            total.messages,
		"messages_per_second": float64(total.messages) / seconds,
		"response_bytes":      total.bytes,
		"mib_per_second":      float64(total.bytes) / seconds / (1024 * 1024),
		"errors":              total.errors,
		"error_samples":       total.errorSample,
		"latency_ms": map[string]float64{
			"p50": percentileMillis(total.latency, 0.50),
			"p95": percentileMillis(total.latency, 0.95),
			"p99": percentileMillis(total.latency, 0.99),
			"max": percentileMillis(total.latency, 1.00),
		},
	}
	if config.rangeBatchSize > 0 {
		result["range_scan"] = map[string]any{
			"start_height":               config.startHeight,
			"end_height":                 config.endHeight,
			"batch_size":                 config.rangeBatchSize,
			"expected_blocks_per_client": config.endHeight - config.startHeight + 1,
			"completed_clients":          completedScans,
			"complete":                   completedScans == config.concurrency,
		}
	}
	if mainnetState != nil {
		result["mainnet_state"] = mainnetState
	}
	return result, nil
}

// scanRequestConfig advances a client through the interval exactly once and
// clips the last batch. iteration must be less than the configured batch count.
func scanRequestConfig(config loadConfig, iteration int) loadConfig {
	start := config.startHeight + uint64(iteration)*config.rangeBatchSize
	count := min(config.rangeBatchSize, config.endHeight-start+1)
	config.startHeight, config.endHeight = start, start+count-1
	return config
}

func executeRequest(ctx context.Context, client walletrpc.CompactTxStreamerClient, config loadConfig, sequence int) (uint64, uint64, error) {
	operation := config.operation
	if operation == "poll" {
		operation = []string{"latest-block", "latest-tree", "info"}[sequence%3]
	}
	if operation == "wallet-poll" {
		operation = []string{"latest-block", "tree", "info"}[sequence%3]
	}
	if operation == "wallet-load" {
		operation = []string{"range", "range", "range", "range", "range", "range", "range", "range", "tree", "tree", "info", "subtree"}[sequence%12]
	}
	switch operation {
	case "block":
		response, err := client.GetBlock(ctx, &walletrpc.BlockID{Height: config.blockHeight})
		if err != nil {
			return 0, 0, err
		}
		return 1, uint64(proto.Size(response)), nil
	case "range":
		stream, err := client.GetBlockRange(ctx, &walletrpc.BlockRange{
			Start:     &walletrpc.BlockID{Height: config.startHeight},
			End:       &walletrpc.BlockID{Height: config.endHeight},
			PoolTypes: config.pools,
		})
		if err != nil {
			return 0, 0, err
		}
		var messages, bytes uint64
		for {
			response, err := stream.Recv()
			if err == io.EOF {
				if config.rangeBatchSize > 0 && messages != config.endHeight-config.startHeight+1 {
					return messages, bytes, fmt.Errorf("range returned %d blocks, expected %d", messages, config.endHeight-config.startHeight+1)
				}
				return messages, bytes, nil
			}
			if err != nil {
				return messages, bytes, err
			}
			if config.rangeBatchSize > 0 && (messages > config.endHeight-config.startHeight || response.Height != config.startHeight+messages) {
				return messages, bytes, fmt.Errorf("unexpected range height %d after %d blocks", response.Height, messages)
			}
			messages++
			bytes += uint64(proto.Size(response))
		}
	case "tree":
		response, err := client.GetTreeState(ctx, &walletrpc.BlockID{Height: config.blockHeight})
		if err != nil {
			return 0, 0, err
		}
		return 1, uint64(proto.Size(response)), nil
	case "latest-tree":
		response, err := client.GetLatestTreeState(ctx, &walletrpc.Empty{})
		if err != nil {
			return 0, 0, err
		}
		return 1, uint64(proto.Size(response)), nil
	case "latest-block":
		response, err := client.GetLatestBlock(ctx, &walletrpc.ChainSpec{})
		if err != nil {
			return 0, 0, err
		}
		return 1, uint64(proto.Size(response)), nil
	case "info":
		response, err := client.GetLightdInfo(ctx, &walletrpc.Empty{})
		if err != nil {
			return 0, 0, err
		}
		return 1, uint64(proto.Size(response)), nil
	case "subtree":
		stream, err := client.GetSubtreeRoots(ctx, &walletrpc.GetSubtreeRootsArg{
			ShieldedProtocol: config.subtreeProtocol,
			MaxEntries:       config.subtreeCount,
		})
		if err != nil {
			return 0, 0, err
		}
		var messages, bytes uint64
		for {
			response, err := stream.Recv()
			if err == io.EOF {
				return messages, bytes, nil
			}
			if err != nil {
				return messages, bytes, err
			}
			messages++
			bytes += uint64(proto.Size(response))
		}
	case "mempool":
		stream, err := client.GetMempoolTx(ctx, &walletrpc.GetMempoolTxRequest{
			ExcludeTxidSuffixes: config.mempoolTxIDs,
		})
		if err != nil {
			return 0, 0, err
		}
		var messages, bytes uint64
		for {
			response, err := stream.Recv()
			if err == io.EOF {
				return messages, bytes, nil
			}
			if err != nil {
				return messages, bytes, err
			}
			messages++
			bytes += uint64(proto.Size(response))
		}
	default:
		return 0, 0, &unknownOperationError{operation: operation}
	}
}

type unknownOperationError struct {
	operation string
}

func (e *unknownOperationError) Error() string {
	return "unknown operation " + e.operation
}

func parsePools(names string) ([]walletrpc.PoolType, error) {
	if names == "" {
		return nil, nil
	}
	values := map[string]walletrpc.PoolType{
		"transparent": walletrpc.PoolType_TRANSPARENT,
		"sapling":     walletrpc.PoolType_SAPLING,
		"orchard":     walletrpc.PoolType_ORCHARD,
		"ironwood":    walletrpc.PoolType_IRONWOOD,
	}
	result := make([]walletrpc.PoolType, 0, 4)
	for _, name := range strings.Split(names, ",") {
		value, ok := values[strings.TrimSpace(name)]
		if !ok {
			return nil, &unknownOperationError{operation: "pool " + name}
		}
		result = append(result, value)
	}
	return result, nil
}

func percentileMillis(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(percentile*float64(len(values)))) - 1
	index = max(0, min(index, len(values)-1))
	return float64(values[index]) / float64(time.Millisecond)
}
