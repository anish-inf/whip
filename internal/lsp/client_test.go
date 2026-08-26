package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadFrameSplit(t *testing.T) {
	// Two frames back to back, the second's header split across reads.
	wire := "Content-Length: 9\r\n\r\n{\"a\":\"b\"}Content-Length: 8\r\n\r\n{\"c\":42}"
	br := bufio.NewReader(strings.NewReader(wire))
	body, err := readFrame(br)
	if err != nil || string(body) != `{"a":"b"}` {
		t.Fatalf("frame 1: %q %v", body, err)
	}
	body, err = readFrame(br)
	if err != nil || string(body) != `{"c":42}` {
		t.Fatalf("frame 2: %q %v", body, err)
	}
}

func TestReadFrameBad(t *testing.T) {
	if _, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: nope\r\n\r\n"))); err == nil {
		t.Fatal("bad length should error")
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("\r\n"))); err == nil {
		t.Fatal("missing length should error")
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: 10\r\n\r\nshort"))); err == nil {
		t.Fatal("short body should error")
	}
}

func TestRequestRoutingAndCancel(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	c := newClient(inW, outR, nil)
	defer c.shutdown()

	// Server side: answer the first request, never answer the second.
	go func() {
		br := bufio.NewReader(inR)
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var msg rpcMessage
		_ = json.Unmarshal(body, &msg)
		resp, _ := json.Marshal(rpcMessage{ID: msg.ID, Result: json.RawMessage(`{"ok":true}`)})
		_, _ = fmt.Fprintf(outW, "Content-Length: %d\r\n\r\n%s", len(resp), resp)
	}()

	var res struct {
		Ok bool `json:"ok"`
	}
	if err := c.request(context.Background(), "test/method", map[string]any{"x": 1}, &res); err != nil || !res.Ok {
		t.Fatalf("request 1: %v %+v", err, res)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.request(ctx, "test/never", nil, nil); err == nil {
		t.Fatal("unanswered request should hit ctx deadline")
	}
}

func TestServerRequestsGetNullAck(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	c := newClient(inW, outR, nil)
	defer c.shutdown()

	// Server sends window/workDoneProgress/create (a request); expect a null
	// result ack so a real server isn't blocked.
	ack := make(chan string, 1)
	go func() {
		br := bufio.NewReader(inR)
		for {
			body, err := readFrame(br)
			if err != nil {
				return
			}
			var msg rpcMessage
			_ = json.Unmarshal(body, &msg)
			if len(msg.ID) > 0 && msg.Method == "" {
				ack <- string(msg.ID)
				return
			}
		}
	}()
	req := `{"jsonrpc":"2.0","id":99,"method":"window/workDoneProgress/create","params":{}}`
	_, _ = fmt.Fprintf(outW, "Content-Length: %d\r\n\r\n%s", len(req), req)
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		t.Fatal("no ack for server request")
	}
}

func TestRPCErrorMessage(t *testing.T) {
	err := &rpcError{Code: -32601, Message: "method not found"}
	if got := err.Error(); got != "rpc error -32601: method not found" {
		t.Fatalf("Error() = %q", got)
	}
}
