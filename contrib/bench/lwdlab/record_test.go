package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type recordingFixture struct {
	scanServer
	canceled chan struct{}
}

func (s *recordingFixture) GetLightdInfo(ctx context.Context, _ *walletrpc.Empty) (*walletrpc.LightdInfo, error) {
	grpc.SetHeader(ctx, metadata.Pairs("fixture-header", "header"))
	grpc.SetTrailer(ctx, metadata.Pairs("fixture-trailer", "trailer"))
	return &walletrpc.LightdInfo{ChainName: "main", BlockHeight: 3471422}, nil
}

func (s *recordingFixture) GetTreeState(context.Context, *walletrpc.BlockID) (*walletrpc.TreeState, error) {
	return nil, status.Error(codes.NotFound, "fixture missing block")
}

func (s *recordingFixture) GetMempoolStream(_ *walletrpc.Empty, stream walletrpc.CompactTxStreamer_GetMempoolStreamServer) error {
	if err := stream.Send(&walletrpc.RawTransaction{Data: []byte{1, 2, 3}}); err != nil {
		return err
	}
	<-stream.Context().Done()
	close(s.canceled)
	return status.FromContextError(stream.Context().Err()).Err()
}

func startTestGRPC(t *testing.T, server *grpc.Server) *grpc.ClientConn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestRecorderPreservesRPCs(t *testing.T) {
	backend := grpc.NewServer()
	fixture := &recordingFixture{canceled: make(chan struct{})}
	walletrpc.RegisterCompactTxStreamerServer(backend, fixture)
	var output bytes.Buffer
	recorder := &rpcRecorder{upstream: startTestGRPC(t, backend), output: json.NewEncoder(&output)}
	proxy := grpc.NewServer(grpc.ForceServerCodec(wireCodec{}), grpc.UnknownServiceHandler(recorder.handle))
	client := walletrpc.NewCompactTxStreamerClient(startTestGRPC(t, proxy))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var header, trailer metadata.MD
	info, err := client.GetLightdInfo(ctx, &walletrpc.Empty{}, grpc.Header(&header), grpc.Trailer(&trailer))
	if err != nil || info.GetBlockHeight() != 3471422 || header.Get("fixture-header")[0] != "header" || trailer.Get("fixture-trailer")[0] != "trailer" {
		t.Fatalf("unary forwarding failed: %v %v %v %v", info, err, header, trailer)
	}
	stream, err := client.GetBlockRange(ctx, &walletrpc.BlockRange{Start: &walletrpc.BlockID{Height: 3450000}, End: &walletrpc.BlockID{Height: 3450002}})
	if err != nil {
		t.Fatal(err)
	}
	for height := uint64(3450000); height <= 3450002; height++ {
		block, err := stream.Recv()
		if err != nil || block.GetHeight() != height {
			t.Fatalf("stream: %v %v", block, err)
		}
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("stream end: %v", err)
	}
	if _, err := client.GetTreeState(ctx, &walletrpc.BlockID{Height: 42}); status.Code(err) != codes.NotFound {
		t.Fatalf("error status changed: %v", err)
	}
	if _, err := client.SendTransaction(ctx, &walletrpc.RawTransaction{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("write RPC accepted: %v", err)
	}
	mempoolCtx, cancelMempool := context.WithCancel(ctx)
	mempool, err := client.GetMempoolStream(mempoolCtx, &walletrpc.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mempool.Recv(); err != nil {
		t.Fatal(err)
	}
	cancelMempool()
	select {
	case <-fixture.canceled:
	case <-ctx.Done():
		t.Fatal("client cancellation did not reach backend")
	}
	// Finite RPC records are written before their terminal status reaches clients.
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	for _, want := range []struct {
		code     string
		messages int
	}{{"OK", 1}, {"OK", 3}, {"NotFound", 0}} {
		var record rpcRecord
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record.Code != want.code || record.Messages != want.messages || len(record.SHA256) != 64 {
			t.Fatalf("incorrect trace: %+v", record)
		}
		if record.Messages == 3 && (record.Request["start"] != float64(3450000) || record.Request["end"] != float64(3450002)) {
			t.Fatalf("incorrect bounds: %v", record.Request)
		}
	}
}

func TestRecorderRejectsPublicEndpoints(t *testing.T) {
	for _, address := range []string{"zec.rocks:443", "0.0.0.0:9067", "192.0.2.1:9067"} {
		if requireLoopback(address) == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}
