// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zcash/lightwalletd/parser"
)

type backendStats struct {
	mutex       sync.Mutex
	methods     map[string]uint64
	connections atomic.Uint64
	requests    atomic.Uint64
	bytesIn     atomic.Uint64
	bytesOut    atomic.Uint64
}

func (s *backendStats) count(method string, bytesIn, bytesOut int) {
	s.requests.Add(1)
	s.bytesIn.Add(uint64(bytesIn))
	s.bytesOut.Add(uint64(bytesOut))
	s.mutex.Lock()
	s.methods[method]++
	s.mutex.Unlock()
}

func (s *backendStats) reset() {
	s.connections.Store(0)
	s.requests.Store(0)
	s.bytesIn.Store(0)
	s.bytesOut.Store(0)
	s.mutex.Lock()
	clear(s.methods)
	s.mutex.Unlock()
}

func (s *backendStats) snapshot() map[string]any {
	s.mutex.Lock()
	methods := make(map[string]uint64, len(s.methods))
	for method, count := range s.methods {
		methods[method] = count
	}
	s.mutex.Unlock()
	return map[string]any{
		"connections": s.connections.Load(),
		"requests":    s.requests.Load(),
		"bytes_in":    s.bytesIn.Load(),
		"bytes_out":   s.bytesOut.Load(),
		"methods":     methods,
	}
}

type rpcRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  any             `json:"error"`
	ID     int             `json:"id"`
}

type backendFixture struct {
	tipHeight       int
	subtreeCount    int
	delay           time.Duration
	rawBlockReply   []byte
	verboseReply    []byte
	chainInfoReply  []byte
	infoReply       []byte
	mempoolReply    []byte
	rawTxReply      []byte
	mempoolTxCount  int
	syntheticTxs    int
	syntheticRawLen int
}

func runBackend(args []string) {
	flags := flag.NewFlagSet("backend", flag.ExitOnError)
	listen := flags.String("listen", "127.0.0.1:18232", "JSON-RPC listen address")
	adminListen := flags.String("admin-listen", "127.0.0.1:18233", "stats/reset listen address")
	tipHeight := flags.Int("tip-height", 2047, "reported chain tip and cache tip")
	subtreeCount := flags.Int("subtrees", 64, "available subtree roots")
	mempoolCount := flags.Int("mempool", 4000, "mempool transaction IDs")
	rawBlockBytes := flags.Int("raw-block-bytes", 1_000_000, "approximate synthetic raw block size")
	blockFixture := flags.String("block-fixture", "testdata/blocks", "raw block fixture file")
	txFixture := flags.String("tx-fixture", "testdata/zip243_raw_tx", "raw transaction fixture file")
	delayUS := flags.Int("delay-us", 0, "delay each JSON-RPC response")
	_ = flags.Parse(args)

	fixture, err := newBackendFixture(*tipHeight, *subtreeCount, *mempoolCount, *rawBlockBytes, *blockFixture, *txFixture)
	if err != nil {
		fatalf("build backend fixture: %v", err)
	}
	fixture.delay = time.Duration(*delayUS) * time.Microsecond
	stats := &backendStats{methods: make(map[string]uint64)}

	rpcServer := &http.Server{Addr: *listen}
	rpcServer.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			stats.connections.Add(1)
		}
	}
	rpcServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var request rpcRequest
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if fixture.delay > 0 {
			time.Sleep(fixture.delay)
		}
		reply := fixture.reply(request)
		stats.count(request.Method, len(body), len(reply))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	})

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats.snapshot())
	})
	adminMux.HandleFunc("/reset", func(w http.ResponseWriter, _ *http.Request) {
		stats.reset()
		w.WriteHeader(http.StatusNoContent)
	})
	adminServer := &http.Server{Addr: *adminListen, Handler: adminMux}

	errCh := make(chan error, 2)
	go func() { errCh <- rpcServer.ListenAndServe() }()
	go func() { errCh <- adminServer.ListenAndServe() }()
	ready := map[string]any{
		"rpc":                  *listen,
		"admin":                *adminListen,
		"tip_height":           fixture.tipHeight,
		"raw_block_bytes":      fixture.syntheticRawLen,
		"raw_block_tx_count":   fixture.syntheticTxs,
		"mempool_transactions": fixture.mempoolTxCount,
	}
	_ = json.NewEncoder(os.Stdout).Encode(ready)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-signals:
		_ = sig
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fatalf("backend server: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = rpcServer.Shutdown(ctx)
	_ = adminServer.Shutdown(ctx)
}

func newBackendFixture(tipHeight, subtreeCount, mempoolCount, targetRawBytes int, blockPath, txPath string) (*backendFixture, error) {
	rawLine, err := firstDataLine(blockPath)
	if err != nil {
		return nil, err
	}
	rawBlock, err := hex.DecodeString(rawLine)
	if err != nil {
		return nil, err
	}
	header := parser.NewBlockHeader()
	rest, err := header.ParseFromSlice(rawBlock)
	if err != nil {
		return nil, err
	}
	headerLen := len(rawBlock) - len(rest)
	parsed := parser.NewBlock()
	if rest, err = parsed.ParseFromSlice(rawBlock); err != nil || len(rest) != 0 {
		return nil, fmt.Errorf("parse block fixture: rest=%d err=%w", len(rest), err)
	}
	txBytes := parsed.Transactions()[0].Bytes()
	txCount := max(1, (targetRawBytes-headerLen-9)/len(txBytes))
	synthetic := make([]byte, 0, headerLen+9+txCount*len(txBytes))
	synthetic = append(synthetic, rawBlock[:headerLen]...)
	synthetic = append(synthetic, compactSize(txCount)...)
	for range txCount {
		synthetic = append(synthetic, txBytes...)
	}
	verify := parser.NewBlock()
	if rest, err = verify.ParseFromSlice(synthetic); err != nil || len(rest) != 0 {
		return nil, fmt.Errorf("parse synthetic block: rest=%d err=%w", len(rest), err)
	}

	const blockID = "0000000000000000000000000000000000000000000000000000000000380640"
	const txID = "1234000000000000000000000000000000000000000000000000000000000000"
	txIDs := make([]string, txCount)
	for i := range txIDs {
		txIDs[i] = txID
	}
	verboseResult, _ := json.Marshal(map[string]any{
		"hash": blockID,
		"tx":   txIDs,
		"trees": map[string]any{
			"sapling":  map[string]uint32{"size": uint32(txCount)},
			"orchard":  map[string]uint32{"size": uint32(txCount)},
			"ironwood": map[string]uint32{"size": uint32(txCount)},
		},
	})
	rawResult, _ := json.Marshal(hex.EncodeToString(synthetic))
	chainResult, _ := json.Marshal(map[string]any{
		"chain":           "regtest",
		"blocks":          tipHeight,
		"bestblockhash":   displayBlockHash(tipHeight),
		"estimatedheight": tipHeight,
		"consensus":       map[string]string{"chaintip": "c8e71055", "nextblock": "c8e71055"},
		"upgrades": map[string]any{
			"76b809bb": map[string]any{"name": "Sapling", "activationheight": 1, "status": "active"},
		},
	})
	infoResult, _ := json.Marshal(map[string]string{"build": "load-lab", "subversion": "/Zebra:load-lab/"})

	permutation := rand.New(rand.NewSource(1)).Perm(mempoolCount)
	mempool := make([]string, mempoolCount)
	for i, index := range permutation {
		mempool[i] = mempoolTxID(index)
	}
	mempoolResult, _ := json.Marshal(mempool)
	rawTx, err := firstDataLine(txPath)
	if err != nil {
		return nil, err
	}
	rawTxResult, _ := json.Marshal(rawTx)

	return &backendFixture{
		tipHeight:       tipHeight,
		subtreeCount:    subtreeCount,
		rawBlockReply:   wrapResult(rawResult),
		verboseReply:    wrapResult(verboseResult),
		chainInfoReply:  wrapResult(chainResult),
		infoReply:       wrapResult(infoResult),
		mempoolReply:    wrapResult(mempoolResult),
		rawTxReply:      wrapResult(rawTxResult),
		mempoolTxCount:  mempoolCount,
		syntheticTxs:    txCount,
		syntheticRawLen: len(synthetic),
	}, nil
}

func (f *backendFixture) reply(request rpcRequest) []byte {
	switch request.Method {
	case "getblockchaininfo":
		return f.chainInfoReply
	case "getbestblockhash":
		result, _ := json.Marshal(displayBlockHash(f.tipHeight))
		return wrapResult(result)
	case "getinfo":
		return f.infoReply
	case "getblock":
		if len(request.Params) > 1 && string(request.Params[1]) == "1" {
			return f.verboseReply
		}
		return f.rawBlockReply
	case "getrawmempool":
		return f.mempoolReply
	case "getrawtransaction":
		return f.rawTxReply
	case "z_gettreestate":
		height := f.tipHeight
		if len(request.Params) > 0 {
			var text string
			if json.Unmarshal(request.Params[0], &text) == nil {
				if parsed, err := strconv.Atoi(text); err == nil {
					height = parsed
				}
			}
		}
		result, _ := json.Marshal(map[string]any{
			"height": height,
			"hash":   displayBlockHash(min(height, f.tipHeight)),
			"time":   1_700_000_000 + height,
			"sapling": map[string]any{
				"commitments": map[string]string{"finalState": strings.Repeat("01", 64)},
			},
			"orchard": map[string]any{
				"commitments": map[string]string{"finalState": strings.Repeat("02", 64)},
			},
			"ironwood": map[string]any{
				"commitments": map[string]string{"finalState": strings.Repeat("03", 64)},
			},
		})
		return wrapResult(result)
	case "z_getsubtreesbyindex":
		start := 0
		maximum := 0
		if len(request.Params) > 1 {
			_ = json.Unmarshal(request.Params[1], &start)
		}
		if len(request.Params) > 2 {
			_ = json.Unmarshal(request.Params[2], &maximum)
		}
		end := f.subtreeCount
		if maximum > 0 && start+maximum < end {
			end = start + maximum
		}
		if start > end {
			start = end
		}
		type subtree struct {
			Root      string `json:"root"`
			EndHeight int    `json:"end_height"`
		}
		subtrees := make([]subtree, 0, end-start)
		spacing := max(1, (f.tipHeight-16)/max(1, f.subtreeCount))
		for index := start; index < end; index++ {
			root := blockHash(100_000 + index)
			subtrees = append(subtrees, subtree{
				Root:      hex.EncodeToString(root[:]),
				EndHeight: min(f.tipHeight, 8+index*spacing),
			})
		}
		result, _ := json.Marshal(map[string]any{"subtrees": subtrees})
		return wrapResult(result)
	default:
		errorBody, _ := json.Marshal(rpcReply{
			Result: json.RawMessage("null"),
			Error:  map[string]any{"code": -32601, "message": "method not found"},
			ID:     1,
		})
		return errorBody
	}
}

func wrapResult(result json.RawMessage) []byte {
	reply, err := json.Marshal(rpcReply{Result: result, ID: 1})
	if err != nil {
		panic(err)
	}
	return reply
}

func firstDataLine(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			return line, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no data in %q", path)
}

func compactSize(value int) []byte {
	switch {
	case value < 253:
		return []byte{byte(value)}
	case value <= 0xffff:
		result := make([]byte, 3)
		result[0] = 253
		binary.LittleEndian.PutUint16(result[1:], uint16(value))
		return result
	default:
		result := make([]byte, 5)
		result[0] = 254
		binary.LittleEndian.PutUint32(result[1:], uint32(value))
		return result
	}
}
