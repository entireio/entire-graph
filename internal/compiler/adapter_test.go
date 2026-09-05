package compiler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCapsuleConfinedAndMissingIdentity(t *testing.T) {
	for _, name := range []string{"../secret", "/absolute", ".git/config", "a/../b", "a\\b"} {
		if dir, _, err := createCapsule(map[string]string{name: "x"}); err == nil {
			os.RemoveAll(dir)
			t.Fatalf("accepted %q", name)
		}
	}
	dir, inputs, err := createCapsule(map[string]string{"go.mod": "module fixture\n", "empty": ""})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	present, missing := false, false
	for _, input := range inputs {
		if input.Path == "empty" {
			present = input.Present && input.Digest == ContentDigest("")
		}
		if input.Path == "go.work" {
			missing = !input.Present && input.Digest == ""
		}
	}
	if !present || !missing {
		t.Fatal("lost missing versus empty")
	}
}
func TestMapLocationExactAndConfined(t *testing.T) {
	files := map[string]string{"a b.go": "a𐐀b\r\n"}
	location := Location{URI: "file:///workspace/a%20b.go", Range: Range{Start: Position{0, 1}, End: Position{0, 3}}}
	file, start, end, err := MapLocation(files, location)
	if err != nil || file != "a b.go" || start != 1 || end != 5 {
		t.Fatalf("map %s %d %d %v", file, start, end, err)
	}
	for _, uri := range []string{"file:///etc/passwd", "file://remote/workspace/a%20b.go", "file:///workspace/../a%20b.go", "file:///workspace/a%20b.go?q=1"} {
		location.URI = uri
		if _, _, _, err := MapLocation(files, location); err == nil {
			t.Fatalf("accepted %s", uri)
		}
	}
}
func TestRPCRejectsServerEditsAndHandlesConfiguration(t *testing.T) {
	local, server := net.Pipe()
	defer local.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newRPCClient(ctx, local, local)
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		if _, err := ReadMessage(reader); err != nil {
			done <- err
			return
		}
		for _, method := range []string{"workspace/applyEdit", "workspace/configuration"} {
			frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "server-request", "method": method, "params": map[string]any{"items": []any{map[string]any{"section": "gopls"}}}})
			if err := WriteMessage(server, frame); err != nil {
				done <- err
				return
			}
			reply, err := ReadMessage(reader)
			if err != nil {
				done <- err
				return
			}
			if method == "workspace/applyEdit" && !strings.Contains(string(reply), `"applied":false`) {
				done <- io.ErrUnexpectedEOF
				return
			}
		}
		done <- WriteMessage(server, json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":[]}`))
	}()
	if _, err := client.request("textDocument/definition", nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(client.diagnostics) != 1 || client.diagnostics[0].Code != "compiler_edit_rejected" {
		t.Fatal("edit rejection not reported")
	}
}

func TestRPCRejectsOutOfOrderAndMalformedReplies(t *testing.T) {
	for _, reply := range []string{`{"jsonrpc":"2.0","id":2,"result":[]}`, `{"jsonrpc":"2.0","id":"1","result":[]}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"private source payload"}}`} {
		t.Run(reply, func(t *testing.T) {
			local, server := net.Pipe()
			defer local.Close()
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := newRPCClient(ctx, local, local)
			go func() { ReadMessage(bufio.NewReader(server)); WriteMessage(server, json.RawMessage(reply)) }()
			_, err := client.request("textDocument/definition", nil)
			if err == nil || strings.Contains(err.Error(), "private source payload") {
				t.Fatalf("unsafe protocol result %v", err)
			}
		})
	}
}
func TestRPCCancellationBoundsUnresponsiveServer(t *testing.T) {
	local, server := net.Pipe()
	defer local.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newRPCClient(ctx, local, local)
	read := make(chan struct{})
	go func() { ReadMessage(bufio.NewReader(server)); close(read) }()
	go func() { <-read; cancel() }()
	if _, err := client.request("initialize", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
func TestRPCConfigurationRetainsBuildTags(t *testing.T) {
	var output bytes.Buffer
	client := &rpcClient{writer: &output, configuration: map[string]any{"buildFlags": []string{"-tags=alternate"}}}
	err := client.serverMessage(context.Background(), rpcMessage{ID: json.RawMessage(`1`), Method: "workspace/configuration", Params: json.RawMessage(`{"items":[{"section":"gopls"}]}`)})
	if err != nil || !strings.Contains(output.String(), "-tags=alternate") {
		t.Fatalf("configuration %s %v", output.String(), err)
	}
}

func TestRPCSendsBoundedCancellationNotification(t *testing.T) {
	local, server := net.Pipe()
	defer local.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newRPCClient(ctx, local, local)
	reply := make(chan []byte, 1)
	go func() {
		reader := bufio.NewReader(server)
		ReadMessage(reader)
		cancel()
		body, _ := ReadMessage(reader)
		reply <- body
	}()
	if _, err := client.request("initialize", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case body := <-reply:
		if !strings.Contains(string(body), `"method":"$/cancelRequest"`) || !strings.Contains(string(body), `"id":1`) {
			t.Fatalf("cancellation %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("no cancellation notification")
	}
}

func TestCompilerRejectsMalformedCapturedOperationIdentity(t *testing.T) {
	if err := validateConfig(Config{OperationID: "not-a-sha256"}); err == nil || !strings.Contains(err.Error(), "operation identity") {
		t.Fatalf("captured identity validation %v", err)
	}
}
