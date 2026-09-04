package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// wireCodec forwards protobuf messages without converting response contents.
type wireCodec struct{}

func (wireCodec) Name() string                          { return "proto" }
func (wireCodec) Marshal(v interface{}) ([]byte, error) { return *v.(*[]byte), nil }
func (wireCodec) Unmarshal(b []byte, v interface{}) error {
	*v.(*[]byte) = append((*v.(*[]byte))[:0], b...)
	return nil
}

type rpcRecord struct {
	Started              time.Time              `json:"started"`
	Method               string                 `json:"method"`
	Request              map[string]interface{} `json:"request,omitempty"`
	Messages             int                    `json:"messages"`
	Bytes                int                    `json:"bytes"`
	FirstResponseSeconds float64                `json:"first_response_seconds"`
	Seconds              float64                `json:"seconds"`
	Code                 string                 `json:"code"`
	SHA256               string                 `json:"response_sha256"`
}

// requestSummary records public chain bounds and counts, never wallet addresses,
// transaction filters, authentication metadata, or full request bodies.
func requestSummary(method string, body []byte) map[string]interface{} {
	result := make(map[string]interface{})
	switch method[strings.LastIndex(method, "/")+1:] {
	case "GetBlockRange", "GetBlockRangeNullifiers":
		var r walletrpc.BlockRange
		if proto.Unmarshal(body, &r) == nil {
			result["start"] = r.GetStart().GetHeight()
			result["end"] = r.GetEnd().GetHeight()
			result["pool_types"] = r.GetPoolTypes()
		}
	case "GetBlock", "GetBlockNullifiers", "GetTreeState":
		var r walletrpc.BlockID
		if proto.Unmarshal(body, &r) == nil {
			result["height"] = r.GetHeight()
		}
	case "GetSubtreeRoots":
		var r walletrpc.GetSubtreeRootsArg
		if proto.Unmarshal(body, &r) == nil {
			result["start_index"] = r.GetStartIndex()
			result["max_entries"] = r.GetMaxEntries()
			result["pool"] = r.GetShieldedProtocol()
		}
	case "GetTaddressTxids", "GetTaddressTransactions":
		var r walletrpc.TransparentAddressBlockFilter
		if proto.Unmarshal(body, &r) == nil {
			result["start"] = r.GetRange().GetStart().GetHeight()
			result["end"] = r.GetRange().GetEnd().GetHeight()
		}
	}
	return result
}

type rpcRecorder struct {
	upstream *grpc.ClientConn
	output   *json.Encoder
	mu       sync.Mutex
	err      error
}

// handle preserves single-request unary and server-streaming RPC contents and
// flow control. Client-streaming and write RPCs are outside this fixture.
func (r *rpcRecorder) handle(_ interface{}, downstream grpc.ServerStream) (returned error) {
	method, ok := grpc.MethodFromServerStream(downstream)
	if !ok {
		return status.Error(codes.Internal, "missing method")
	}
	if !strings.HasPrefix(method, "/cash.z.wallet.sdk.rpc.CompactTxStreamer/Get") || strings.HasSuffix(method, "BalanceStream") {
		return status.Error(codes.Unimplemented, "recorder supports single-request read RPCs")
	}
	r.mu.Lock()
	outputError := r.err
	r.mu.Unlock()
	if outputError != nil {
		return status.Error(codes.Internal, "trace output failed")
	}
	start := time.Now()
	record := rpcRecord{Started: start.UTC(), Method: method}
	digest := sha256.New()
	defer func() {
		record.Seconds = time.Since(start).Seconds()
		record.Code = status.Code(returned).String()
		record.SHA256 = hex.EncodeToString(digest.Sum(nil))
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.output.Encode(record); err != nil {
			r.err = err
			returned = status.Error(codes.Internal, "trace output failed")
		}
	}()
	var request []byte
	if err := downstream.RecvMsg(&request); err != nil {
		return err
	}
	record.Request = requestSummary(method, request)
	var extra []byte
	if err := downstream.RecvMsg(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return status.Error(codes.InvalidArgument, "expected one request")
	}
	ctx, cancel := context.WithCancel(downstream.Context())
	defer cancel()
	upstream, err := r.upstream.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, method, grpc.ForceCodec(wireCodec{}))
	if err != nil {
		return err
	}
	if err = upstream.SendMsg(&request); err != nil {
		return err
	}
	if err = upstream.CloseSend(); err != nil {
		return err
	}
	header, err := upstream.Header()
	if err != nil {
		return err
	}
	if err = downstream.SendHeader(header); err != nil {
		return err
	}
	defer func() { downstream.SetTrailer(upstream.Trailer()) }()
	for {
		var response []byte
		err = upstream.RecvMsg(&response)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if record.Messages == 0 {
			record.FirstResponseSeconds = time.Since(start).Seconds()
		}
		if err = downstream.SendMsg(&response); err != nil {
			return err
		}
		record.Messages++
		record.Bytes += len(response)
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(response)))
		digest.Write(size[:])
		digest.Write(response)
	}
}

func requireLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("expected a numeric loopback address, got %q", address)
	}
	return nil
}

func runRecord(args []string) {
	flags := flag.NewFlagSet("record", flag.ExitOnError)
	listen := flags.String("listen", "127.0.0.1:19068", "loopback listener")
	address := flags.String("upstream", "127.0.0.1:19067", "isolated lightwalletd on loopback")
	output := flags.String("output", "wallet-rpcs.jsonl", "new trace file")
	flags.Parse(args)
	for _, address := range []string{*listen, *address} {
		if err := requireLoopback(address); err != nil {
			panic(err)
		}
	}
	f, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	conn, err := grpc.NewClient(*address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20)))
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	recorder := rpcRecorder{upstream: conn, output: json.NewEncoder(f)}
	server := grpc.NewServer(grpc.ForceServerCodec(wireCodec{}), grpc.UnknownServiceHandler(recorder.handle))
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(os.Stderr, "Recording disposable wallet RPCs on %s through %s\n", *listen, *address)
	if err = server.Serve(listener); err != nil {
		panic(err)
	}
}
