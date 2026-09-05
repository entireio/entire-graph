package compiler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const RequestTimeout = 5 * time.Second
const OperationTimeout = 30 * time.Second
const MaxQueries = 500

// rpcClient is a single-flight, bounded stdio client. A request timeout poisons
// the session: callers terminate its process tree instead of reusing late data.
type rpcClient struct {
	ctx           context.Context
	writer        io.Writer
	writeMu       sync.Mutex
	messages      chan rpcRead
	next          int
	notifications int
	diagnostics   []Diagnostic
	configuration map[string]any
}
type rpcRead struct {
	bytes json.RawMessage
	err   error
}
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newRPCClient(ctx context.Context, reader io.Reader, writer io.Writer) *rpcClient {
	client := &rpcClient{ctx: ctx, writer: writer, messages: make(chan rpcRead, 1)}
	go func() {
		reader := bufio.NewReaderSize(reader, maxHeaderBytes)
		for {
			body, err := ReadMessage(reader)
			select {
			case client.messages <- rpcRead{body, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return client
}
func (client *rpcClient) send(ctx context.Context, message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(body) > MaxMessageBytes {
		return errors.New("compiler message limit")
	}
	done := make(chan error, 1)
	go func() {
		client.writeMu.Lock()
		defer client.writeMu.Unlock()
		done <- WriteMessage(client.writer, body)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (client *rpcClient) notify(method string, params any) error {
	ctx, cancel := context.WithTimeout(client.ctx, RequestTimeout)
	defer cancel()
	return client.send(ctx, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (client *rpcClient) request(method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(client.ctx, RequestTimeout)
	defer cancel()
	client.next++
	id := client.next
	if err := client.send(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		// The peer may have received the request even when cancellation wins
		// the send completion race. Its identifier is still safe to cancel.
		if ctx.Err() != nil {
			client.cancelRequest(id)
		}
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			client.cancelRequest(id)
			return nil, ctx.Err()
		case read := <-client.messages:
			if read.err != nil {
				return nil, read.err
			}
			var message rpcMessage
			if err := json.Unmarshal(read.bytes, &message); err != nil {
				return nil, err
			}
			if message.Method != "" {
				client.notifications++
				if client.notifications > 10000 {
					return nil, errors.New("compiler notification limit")
				}
				if err := client.serverMessage(ctx, message); err != nil {
					return nil, err
				}
				continue
			}
			var responseID int
			if json.Unmarshal(message.ID, &responseID) != nil || responseID != id {
				return nil, errors.New("unexpected compiler response identifier")
			}
			if message.Error != nil {
				return nil, fmt.Errorf("compiler request %s failed with code %d", method, message.Error.Code)
			}
			return message.Result, nil
		}
	}
}
func (client *rpcClient) serverMessage(ctx context.Context, message rpcMessage) error {
	if len(message.ID) == 0 {
		if message.Method == "textDocument/publishDiagnostics" {
			var params struct {
				URI         string `json:"uri"`
				Diagnostics []struct {
					Message  string `json:"message"`
					Severity int    `json:"severity"`
				} `json:"diagnostics"`
			}
			if json.Unmarshal(message.Params, &params) != nil {
				return errors.New("malformed compiler diagnostics")
			}
			for _, diagnostic := range params.Diagnostics {
				if diagnostic.Severity == 1 && len(client.diagnostics) < 100 {
					client.diagnostics = append(client.diagnostics, Diagnostic{Code: "compiler_diagnostic", Detail: "server reported an error in analyzed source"})
				}
			}
		}
		return nil
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": message.ID}
	switch message.Method {
	case "workspace/configuration":
		var params struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(message.Params, &params) != nil || len(params.Items) > 100 {
			return errors.New("compiler configuration request limit")
		}
		values := make([]any, len(params.Items))
		for i := range values {
			values[i] = client.configuration
			if values[i] == nil {
				values[i] = map[string]any{"telemetryPrompt": false, "checkUpdates": "off", "staticcheck": false}
			}
		}
		reply["result"] = values
	case "workspace/applyEdit":
		reply["result"] = map[string]any{"applied": false, "failureReason": "read-only compiler analysis"}
		client.diagnostics = append(client.diagnostics, Diagnostic{Code: "compiler_edit_rejected", Detail: "server requested an edit; source remained read-only"})
	case "window/showMessageRequest":
		reply["result"] = nil
	default:
		reply["error"] = rpcError{Code: -32601, Message: "unsupported read-only client request"}
	}
	return client.send(ctx, reply)
}

func (client *rpcClient) cancelRequest(id int) {
	// Protocol cancellation is best effort; process-tree teardown is final.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = client.send(ctx, map[string]any{"jsonrpc": "2.0", "method": "$/cancelRequest", "params": map[string]any{"id": id}})
}
