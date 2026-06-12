// Copyright (c) 2019-2020 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package frontend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/zcash/lightwalletd/common"
	ini "gopkg.in/ini.v1"
)

// NewZRPCFromConf reads the zcashd configuration file and returns the parsed
// connection config.
func NewZRPCFromConf(confPath string) (*rpcclient.ConnConfig, error) {
	return connFromConf(confPath)
}

// NewZRPCFromFlags gets zcashd rpc connection information from provided flags.
func NewZRPCFromFlags(opts *common.Options) (*rpcclient.ConnConfig, error) {
	// Connect to local Zcash RPC server using HTTP POST mode.
	connCfg := &rpcclient.ConnConfig{
		Host:         net.JoinHostPort(opts.RPCHost, opts.RPCPort),
		User:         opts.RPCUser,
		Pass:         opts.RPCPassword,
		HTTPPostMode: true, // Zcash only supports HTTP POST mode
		DisableTLS:   true, // Zcash does not provide TLS by default
	}
	return connCfg, nil
}

// zrpcRequest / zrpcError / zrpcResponse mirror the JSON-RPC 1.0 wire format
// that zcashd/zebrad speak.
type zrpcRequest struct {
	Jsonrpc string            `json:"jsonrpc"`
	ID      uint64            `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

// zrpcError matches btcjson.RPCError's Error() formatting ("<code>: <message>")
// so existing callers that string-match the code (e.g. "-8" for an unknown
// height in common.getBlockFromRPC) keep working unchanged.
type zrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *zrpcError) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Message) }

type zrpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *zrpcError      `json:"error"`
}

// NewZcashRPCRawRequester returns a common.RawRequest-compatible function backed
// by a connection-pooled net/http client. Unlike the btcd rpcclient (which
// funnels every HTTP POST through a single goroutine, serializing all RPCs),
// this issues each request independently, so concurrent callers — notably the
// parallel block ingestor — actually achieve concurrency against the backend.
// maxConns bounds the connection pool to the backend.
func NewZcashRPCRawRequester(cfg *rpcclient.ConnConfig, maxConns int) func(string, []json.RawMessage) (json.RawMessage, error) {
	scheme := "https"
	if cfg.DisableTLS {
		scheme = "http"
	}
	url := scheme + "://" + cfg.Host + "/"
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.User+":"+cfg.Pass))
	if maxConns < 1 {
		maxConns = 1
	}
	transport := &http.Transport{
		MaxIdleConns:        maxConns,
		MaxIdleConnsPerHost: maxConns,
		MaxConnsPerHost:     maxConns,
		IdleConnTimeout:     90 * time.Second,
	}
	httpClient := &http.Client{Transport: transport, Timeout: 120 * time.Second}
	var idCounter uint64

	return func(method string, params []json.RawMessage) (json.RawMessage, error) {
		if params == nil {
			params = []json.RawMessage{}
		}
		reqBody, err := json.Marshal(zrpcRequest{
			Jsonrpc: "1.0",
			ID:      atomic.AddUint64(&idCounter, 1),
			Method:  method,
			Params:  params,
		})
		if err != nil {
			return nil, err
		}
		httpReq, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", auth)

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var r zrpcResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("error unmarshalling RPC response (HTTP %d): %w: %s",
				resp.StatusCode, err, string(body))
		}
		if r.Error != nil {
			return nil, r.Error
		}
		return r.Result, nil
	}
}

func connFromConf(confPath string) (*rpcclient.ConnConfig, error) {
	if filepath.Ext(confPath) == ".toml" {
		return connFromToml(confPath)
	} else {
		return connFromIni(confPath)
	}
}

func connFromIni(confPath string) (*rpcclient.ConnConfig, error) {
	cfg, err := ini.Load(confPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file in .conf format: %w", err)
	}

	rpcaddr := cfg.Section("").Key("rpcbind").String()
	if rpcaddr == "" {
		rpcaddr = "127.0.0.1"
	}
	rpcport := cfg.Section("").Key("rpcport").String()
	if rpcport == "" {
		rpcport = "8232" // default mainnet
		testnet, _ := cfg.Section("").Key("testnet").Int()
		regtest, _ := cfg.Section("").Key("regtest").Int()
		if testnet > 0 || regtest > 0 {
			rpcport = "18232"
		}
	}
	username := cfg.Section("").Key("rpcuser").String()
	password := cfg.Section("").Key("rpcpassword").String()

	if password == "" {
		return nil, errors.New("rpcpassword not found (or empty), please add rpcpassword= to zcash.conf")
	}

	// Connect to local Zcash RPC server using HTTP POST mode.
	connCfg := &rpcclient.ConnConfig{
		Host:         net.JoinHostPort(rpcaddr, rpcport),
		User:         username,
		Pass:         password,
		HTTPPostMode: true, // Zcash only supports HTTP POST mode
		DisableTLS:   true, // Zcash does not provide TLS by default
	}
	// Notice the notification parameter is nil since notifications are
	// not supported in HTTP POST mode.
	return connCfg, nil
}

// If passed a string, interpret as a path, open and read; if passed
// a byte slice, interpret as the config file content (used in testing).
func connFromToml(confPath string) (*rpcclient.ConnConfig, error) {
	var tomlConf struct {
		Rpc struct {
			Listen_addr string
			RPCUser     string
			RPCPassword string
		}
	}
	_, err := toml.DecodeFile(confPath, &tomlConf)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file in .toml format: %w", err)
	}
	conf := rpcclient.ConnConfig{
		Host:         tomlConf.Rpc.Listen_addr,
		User:         tomlConf.Rpc.RPCUser,
		Pass:         tomlConf.Rpc.RPCPassword,
		HTTPPostMode: true, // Zcash only supports HTTP POST mode
		DisableTLS:   true, // Zcash does not provide TLS by default
	}

	// Notice the notification parameter is nil since notifications are
	// not supported in HTTP POST mode.
	return &conf, nil
}
