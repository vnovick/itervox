// Package mcpserver is `itervox mcp` (#216): a local Model Context Protocol
// server over stdio that lets an MCP client (Claude Code, Claude Desktop,
// Codex) read the board and memory of a running daemon and, with an operator
// token, run the fleet.
//
// It is a plain HTTP client of the daemon's /api/v1: every tool is one or a
// few API calls made with the token `itervox mcp` was given, so the daemon's
// scope checks, redaction and approval rules apply unchanged. It never reads
// orchestrator state directly.
//
// The protocol is a small JSON-RPC 2.0 implementation of the MCP stdio
// transport (initialize, notifications/initialized, ping, tools/list,
// tools/call): one JSON message per line on stdin and stdout. Stdout carries
// protocol messages only; logs go to the logger (stderr).
//
// The package imports internal/apitokens (scope order) and internal/logging
// (secret redaction) only.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
)

// ProtocolVersions are the MCP protocol revisions this server speaks,
// newest first. initialize echoes the client's revision when it is listed
// and answers with the newest otherwise (the client then decides).
var ProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// maxMessageBytes bounds one incoming message (a line).
const maxMessageBytes = 4 << 20

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Server is one MCP session over a daemon's API.
type Server struct {
	// API is the daemon's API client.
	API *Client
	// Version is reported as serverInfo.version.
	Version string
	// MaxScope caps the scope the tools are listed and allowed for; empty:
	// the token's own scope (GET /api/v1/whoami). `itervox mcp` sets viewer
	// unless asked for operator, so a daemon running without authentication
	// (whose callers are admin) still serves read tools only by default.
	MaxScope string
	// Logger receives diagnostics; nil discards them. It must never write
	// to the protocol stream.
	Logger *slog.Logger

	writeMu sync.Mutex
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Logger
}

// Serve reads messages from in and writes responses to out until in ends
// (the client closed stdin) or ctx is done. Requests are handled one at a
// time, in order.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), maxMessageBytes)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		if resp := s.handle(ctx, line); resp != nil {
			if err := s.write(out, resp); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("mcpserver: read: %w", err)
	}
	return nil
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (s *Server) write(out io.Writer, resp *response) error {
	raw, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("mcpserver: encode: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = out.Write(append(raw, '\n'))
	return err
}

// handle answers one message; nil for a notification (no id) or a response
// sent by the client.
func (s *Server) handle(ctx context.Context, line []byte) *response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		if trimmed := trimSpace(line); len(trimmed) > 0 && trimmed[0] == '[' {
			return errResponse(nil, codeInvalidRequest, "batches are not supported")
		}
		return errResponse(nil, codeParseError, "parse error")
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	if req.JSONRPC != "2.0" || req.Method == "" {
		if req.Method == "" && !isNotification {
			return nil // a response to a request we never sent
		}
		if isNotification {
			return nil
		}
		return errResponse(req.ID, codeInvalidRequest, "invalid request")
	}
	if isNotification {
		// notifications/initialized, notifications/cancelled, …: nothing
		// to answer. Requests run one at a time, so a cancel always
		// arrives after its request finished.
		return nil
	}
	result, rerr := s.dispatch(ctx, req)
	if rerr != nil {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: rerr}
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func errResponse(id json.RawMessage, code int, msg string) *response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, &rpcError{Code: codeInvalidParams, Message: "invalid initialize params"}
			}
		}
		version := ProtocolVersions[0]
		if slices.Contains(ProtocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "itervox", "version": s.Version},
			"instructions": "Itervox board and memory for this project. Read tools need a viewer token; " +
				"write tools (dispatch, pause, replies, steering, remember) appear only for an operator token " +
				"and keep the dashboard's rules: guardrail approvals stay human-only and remembered " +
				"items wait in the lessons review queue.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		scope, err := s.Scope(ctx)
		if err != nil {
			// The client still connects (some refuse a server whose
			// tools/list fails); the read tools report the problem when
			// called, and write tools wait until the scope is known.
			s.logger().Warn("mcp: could not read the token's scope; listing read tools only", "error", err)
			scope = scopeViewer
		}
		list := []map[string]any{}
		for _, t := range tools {
			if allowed(scope, t.scope) {
				list = append(list, map[string]any{"name": t.name, "description": t.description, "inputSchema": t.schema})
			}
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params"}
		}
		t, ok := toolByName(p.Name)
		if !ok {
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
		}
		return s.call(ctx, t, p.Arguments), nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
}

// call runs a tool. Tool failures are results with isError set, so the
// model sees them; only protocol problems are JSON-RPC errors.
func (s *Server) call(ctx context.Context, t tool, args json.RawMessage) map[string]any {
	scope, err := s.Scope(ctx)
	if err != nil {
		return errorResult("the itervox daemon is not reachable: " + err.Error())
	}
	if !allowed(scope, t.scope) {
		return errorResult(t.name + " needs an operator token; this server runs with a " + scope +
			" token (restart it with --token-scope operator)")
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	out, err := t.run(ctx, s.API, args)
	if err != nil {
		var refused *refusal
		if !errors.As(err, &refused) {
			s.logger().Info("mcp: tool failed", "tool", t.name, "error", err)
		}
		return errorResult(redact(err.Error()))
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return errorResult("could not encode the result")
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(raw)}}}
}

func errorResult(msg string) map[string]any {
	return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": msg}}}
}

// Scope is the effective scope: the token's (GET /api/v1/whoami), capped
// by MaxScope. It is read on every tools/list and tools/call, so a rotated
// or revoked token takes effect at once.
func (s *Server) Scope(ctx context.Context) (string, error) {
	var who struct {
		Scope string `json:"scope"`
	}
	if err := s.API.Do(ctx, "GET", "/whoami", nil, nil, &who); err != nil {
		return "", err
	}
	return capScope(who.Scope, s.MaxScope), nil
}
