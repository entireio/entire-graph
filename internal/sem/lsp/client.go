// Package lsp is entire-sem's optional, parallel call-resolution path: a minimal
// LSP client that drives a language's own analyzer (e.g. rust-analyzer) to
// resolve calls the tree-sitter heuristic cannot — generic/trait dispatch and
// macro-hidden calls. It is opt-in; the heuristic resolver remains the default.
//
// The client is a port of brain-bench's proven oracle-rust client, which encodes
// the two hard-won lessons: wait for the semantic index (cachePriming end, not
// the earlier syntax-ready signal) and bound every request with skip-on-stall so
// one wedged symbol cannot sink the whole run. A dedicated reader goroutine
// dispatches messages so request/response correlation, server-request acks, and
// progress tracking do not depend on message arrival order.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client is a single LSP session over a server subprocess's stdio.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	writeMu sync.Mutex

	mu          sync.Mutex
	nextID      int
	titles      map[string]string // progress token -> title (from begin)
	endedSignal []string          // tokens/titles whose progress has ended
	diags       map[string]int
	waiters     map[int]chan json.RawMessage
}

// debugLog, when DEBUG_LSP is set, surfaces progress signals so each server's
// readiness markers can be discovered/tuned.
var debugLSP = os.Getenv("DEBUG_LSP") != ""

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   json.RawMessage  `json:"error,omitempty"`
}

// Start launches the server subprocess in the repo directory and starts the
// background reader.
func Start(ctx context.Context, command string, args []string, repoDir string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = repoDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil // servers are chatty; must not block on a full stderr pipe
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1<<20),
		titles:  map[string]string{},
		diags:   map[string]int{},
		waiters: map[int]chan json.RawMessage{},
	}
	go c.readLoop()
	return c, nil
}

func pathToURI(p string) string {
	abs, _ := filepath.Abs(p)
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

func uriToPath(u string) string {
	if parsed, err := url.Parse(u); err == nil && parsed.Scheme == "file" {
		return parsed.Path
	}
	return strings.TrimPrefix(u, "file://")
}

func (c *Client) writeMessage(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(b)); err != nil {
		return err
	}
	_, err = c.stdin.Write(b)
	return err
}

func (c *Client) notify(method string, params any) error {
	return c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) readMessage() (*rpcMessage, error) {
	var contentLen int
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		if k, v, ok := strings.Cut(trimmed, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			contentLen, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if contentLen == 0 {
		return &rpcMessage{}, nil
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(c.stdout, body); err != nil {
		return nil, err
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// readLoop continuously reads server messages and dispatches them: responses go
// to the waiting request's channel; server->client requests are auto-acked (so
// the server cannot deadlock on us); $/progress and diagnostics update state.
func (c *Client) readLoop() {
	for {
		msg, err := c.readMessage()
		if err != nil {
			return // pipe closed / server gone
		}
		// Server->client request: has both id and method. Ack with null result.
		if msg.ID != nil && msg.Method != "" {
			_ = c.writeMessage(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
			continue
		}
		switch msg.Method {
		case "$/progress":
			var p struct {
				Token any `json:"token"`
				Value struct {
					Kind  string `json:"kind"`
					Title string `json:"title"`
				} `json:"value"`
			}
			if json.Unmarshal(msg.Params, &p) == nil {
				tok := fmt.Sprintf("%v", p.Token)
				c.mu.Lock()
				switch p.Value.Kind {
				case "begin":
					c.titles[tok] = p.Value.Title
					if debugLSP {
						fmt.Fprintf(os.Stderr, "[lsp] progress begin token=%q title=%q\n", tok, p.Value.Title)
					}
				case "end":
					c.endedSignal = append(c.endedSignal, tok)
					if t := c.titles[tok]; t != "" {
						c.endedSignal = append(c.endedSignal, t)
					}
					if debugLSP {
						fmt.Fprintf(os.Stderr, "[lsp] progress end   token=%q title=%q\n", tok, c.titles[tok])
					}
				}
				c.mu.Unlock()
			}
		case "textDocument/publishDiagnostics":
			var p struct {
				URI         string `json:"uri"`
				Diagnostics []struct {
					Severity int `json:"severity"`
				} `json:"diagnostics"`
			}
			if json.Unmarshal(msg.Params, &p) == nil {
				n := 0
				for _, d := range p.Diagnostics {
					if d.Severity == 1 {
						n++
					}
				}
				c.mu.Lock()
				c.diags[uriToPath(p.URI)] = n
				c.mu.Unlock()
			}
		}
		// Response to one of our requests (id present, no method).
		if msg.ID != nil && msg.Method == "" {
			if id, err := strconv.Atoi(strings.Trim(string(*msg.ID), `"`)); err == nil {
				c.mu.Lock()
				ch := c.waiters[id]
				delete(c.waiters, id)
				c.mu.Unlock()
				if ch != nil {
					ch <- msg.Result
				}
			}
		}
	}
}

// request sends a request and waits for its response or the timeout. On timeout
// it returns an error so the caller can skip-on-stall.
func (c *Client) request(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.waiters[id] = ch
	c.mu.Unlock()

	if err := c.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.waiters, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("lsp request %q timed out after %s", method, timeout)
	}
}

// sawReady reports whether any ended progress token/title contains one of the
// readiness markers (case-insensitive).
func (c *Client) sawReady(markers []string) bool {
	if len(markers) == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sig := range c.endedSignal {
		low := strings.ToLower(sig)
		for _, m := range markers {
			if strings.Contains(low, strings.ToLower(m)) {
				return true
			}
		}
	}
	return false
}

// waitReady blocks until a server-specific readiness marker is observed (the
// semantic index has primed), then settles briefly so a later indexing pass
// can't change call-hierarchy answers mid-run. When the server emits no known
// marker, it falls back to a fixed warmup. maxWait caps the total wait.
func (c *Client) waitReady(markers []string, warmup, maxWait, settle time.Duration) {
	start := time.Now()
	for time.Since(start) < maxWait {
		if c.sawReady(markers) {
			time.Sleep(settle)
			return
		}
		if len(markers) == 0 && time.Since(start) >= warmup {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(settle)
}

// loadErrors returns the count of error diagnostics over the given files.
func (c *Client) loadErrors(isCompiled func(path string) bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for p, n := range c.diags {
		if isCompiled(p) {
			total += n
		}
	}
	return total
}

// Close terminates the server.
func (c *Client) Close() {
	_ = c.notify("exit", nil)
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}
