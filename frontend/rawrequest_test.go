// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package frontend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/rpcclient"
)

func TestContextRawRequestSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(body) == "" {
			t.Error("empty request body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ok","error":null}`))
	}))
	defer server.Close()

	cfg := &rpcclient.ConnConfig{
		Host:         server.Listener.Addr().String(),
		User:         "user",
		Pass:         "pass",
		HTTPPostMode: true,
		DisableTLS:   true,
	}
	rawRequest, err := NewContextRawRequest(cfg)
	if err != nil {
		t.Fatalf("NewContextRawRequest failed: %v", err)
	}

	result, err := rawRequest(context.Background(), "getblockchaininfo", nil)
	if err != nil {
		t.Fatalf("RawRequest failed: %v", err)
	}
	var reply string
	if err := json.Unmarshal(result, &reply); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if reply != "ok" {
		t.Fatalf("unexpected result %q", reply)
	}
}

func TestContextRawRequestReusesConnection(t *testing.T) {
	var newConnections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ok","error":null}`))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	cfg := &rpcclient.ConnConfig{
		Host:         server.Listener.Addr().String(),
		HTTPPostMode: true,
		DisableTLS:   true,
	}
	rawRequest, err := NewContextRawRequest(cfg)
	if err != nil {
		t.Fatalf("NewContextRawRequest failed: %v", err)
	}

	for range 20 {
		if _, err := rawRequest(context.Background(), "getblockchaininfo", nil); err != nil {
			t.Fatalf("RawRequest failed: %v", err)
		}
	}
	if got := newConnections.Load(); got != 1 {
		t.Fatalf("opened %d backend connections for 20 sequential requests, want 1", got)
	}
}

// TestContextRawRequestCancel verifies that cancelling the context aborts an
// in-flight request promptly, rather than blocking until the zcashd RPC returns.
// This is the core guarantee of the fix for GHSA-5h96-xw2v-jxgq.
func TestContextRawRequestCancel(t *testing.T) {
	var once sync.Once
	started := make(chan struct{})
	// release lets us unblock the handler at teardown so server.Close() does not
	// hang: a client-side context cancel aborts the request but does not
	// necessarily fire the server's r.Context().Done() promptly.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	cfg := &rpcclient.ConnConfig{
		Host:         server.Listener.Addr().String(),
		User:         "user",
		Pass:         "pass",
		HTTPPostMode: true,
		DisableTLS:   true,
	}
	rawRequest, err := NewContextRawRequest(cfg)
	if err != nil {
		t.Fatalf("NewContextRawRequest failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := rawRequest(ctx, "getblockchaininfo", nil)
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the server")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rawRequest did not return promptly after cancellation")
	}
}
