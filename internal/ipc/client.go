package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client is a one-shot unix-socket client. Each Call opens a connection,
// writes a single request, reads a single response, and closes. Simpler
// than connection pooling and fine for CLI-scale usage.
type Client struct {
	Socket  string
	Timeout time.Duration
}

func NewClient(socket string) *Client {
	return &Client{Socket: socket, Timeout: 10 * time.Second}
}

// Call sends the request and decodes the result into out (which may be nil
// if the caller does not need the payload).
func (c *Client) Call(method string, params, out any) error {
	conn, err := net.DialTimeout("unix", c.Socket, c.Timeout)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.Socket, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.Timeout))

	req := Request{Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	if err := json.NewEncoder(conn).Encode(&req); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	var resp Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("daemon: %s", resp.Error)
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

// IsReachable returns true if a ping round-trips within the default timeout.
// Callers use this to decide between daemon IPC and a direct DB read.
func (c *Client) IsReachable() bool {
	return c.Call(MethodPing, nil, nil) == nil
}
