package watchman

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

// Client is a thin JSON-over-unix-socket watchman client. It exposes
// only what mycelium needs: one-shot commands (watch-project,
// subscribe, unsubscribe) plus a streamed channel of incoming PDUs
// that carry either command results or subscription deliveries.
//
// Watchman's JSON PDU framing is one PDU per line — each JSON object
// is terminated by a newline. That lets us use bufio.Scanner with a
// generous buffer for the read side and an encoder that appends '\n'
// on the write side.
type Client struct {
	conn net.Conn
	w    *bufio.Writer
	wMu  sync.Mutex // serializes command writes

	// Read side runs a single goroutine that demultiplexes PDUs:
	// command responses go to requestResp; subscription deliveries
	// go to deliveries. This keeps the ordering straight — watchman
	// may interleave a subscription notification and a command
	// response on one connection.
	deliveries chan RawPDU
	readErr    chan error
	stopOnce   sync.Once
	done       chan struct{}

	reqMu    sync.Mutex
	requests []chan RawPDU
}

// RawPDU is a not-yet-interpreted JSON PDU. We keep it raw here and
// let higher layers Unmarshal into a typed struct of their choosing.
// That keeps the client agnostic to the schema of every watchman
// command we might ever send.
type RawPDU json.RawMessage

// Dial opens a unix socket client against the given watchman sockname
// and starts the read pump. The caller must Close() the returned
// Client when done.
func Dial(sockname string) (*Client, error) {
	conn, err := net.Dial("unix", sockname)
	if err != nil {
		return nil, fmt.Errorf("watchman dial: %w", err)
	}
	return newClient(conn), nil
}

// newClient is extracted so tests can inject net.Pipe pairs.
func newClient(conn net.Conn) *Client {
	c := &Client{
		conn:       conn,
		w:          bufio.NewWriter(conn),
		deliveries: make(chan RawPDU, 64),
		readErr:    make(chan error, 1),
		done:       make(chan struct{}),
	}
	go c.readPump()
	return c
}

// Close tears down the connection. Safe to call multiple times.
func (c *Client) Close() error {
	c.stopOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
	return nil
}

// Send writes one JSON-array command. Commands are written as a
// single newline-terminated JSON document; the caller supplies the
// array (watchman's command shape is always `[cmd, args...]`).
//
// Returns the parsed response PDU or an error if the connection dies
// before one arrives.
func (c *Client) Send(cmd []any) (RawPDU, error) {
	respCh := c.expectResponse()
	defer c.dropResponse(respCh)

	c.wMu.Lock()
	enc := json.NewEncoder(c.w)
	if err := enc.Encode(cmd); err != nil {
		c.wMu.Unlock()
		return nil, fmt.Errorf("watchman encode: %w", err)
	}
	if err := c.w.Flush(); err != nil {
		c.wMu.Unlock()
		return nil, fmt.Errorf("watchman flush: %w", err)
	}
	c.wMu.Unlock()

	select {
	case pdu := <-respCh:
		return pdu, nil
	case err := <-c.readErr:
		return nil, err
	case <-c.done:
		return nil, fmt.Errorf("watchman: connection closed")
	}
}

// Deliveries is the channel of subscription-delivery PDUs. These
// PDUs have a "subscription" field that identifies which subscription
// fired.
func (c *Client) Deliveries() <-chan RawPDU { return c.deliveries }

// ReadErrors surfaces fatal read-loop errors (EOF, short read, JSON
// garble). After a read error the client is unusable — close it.
func (c *Client) ReadErrors() <-chan error { return c.readErr }

// readPump owns the single reader on the socket. Every PDU that has
// a "subscription" key is routed to Deliveries; every other PDU is
// treated as the reply to the oldest pending Send and routed to the
// head of the request queue.
func (c *Client) readPump() {
	defer close(c.deliveries)
	sc := bufio.NewScanner(c.conn)
	// Watchman PDUs can be large on the initial `files` payload; raise
	// the buffer from bufio's 64 KB default to a few MB.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		// Peek at just "subscription" to decide routing. Cheap string
		// scan beats a double-parse.
		if isDelivery(line) {
			cp := make([]byte, len(line))
			copy(cp, line)
			select {
			case c.deliveries <- RawPDU(cp):
			case <-c.done:
				return
			}
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		c.dispatchResponse(RawPDU(cp))
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	select {
	case c.readErr <- err:
	default:
	}
	// Wake any Send()s waiting on a reply.
	c.reqMu.Lock()
	for _, ch := range c.requests {
		close(ch)
	}
	c.requests = nil
	c.reqMu.Unlock()
}

// expectResponse registers a channel that will receive the next
// non-delivery PDU. FIFO — request/response is strictly serialized
// on the connection because we serialize writes under wMu.
func (c *Client) expectResponse() chan RawPDU {
	ch := make(chan RawPDU, 1)
	c.reqMu.Lock()
	c.requests = append(c.requests, ch)
	c.reqMu.Unlock()
	return ch
}

// dropResponse removes a pending response channel (called in Send's
// defer to tolerate early error returns).
func (c *Client) dropResponse(ch chan RawPDU) {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	for i, r := range c.requests {
		if r == ch {
			c.requests = append(c.requests[:i], c.requests[i+1:]...)
			return
		}
	}
}

func (c *Client) dispatchResponse(pdu RawPDU) {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	if len(c.requests) == 0 {
		// Unexpected PDU with no pending request — drop it. Watchman
		// doesn't push unsolicited command responses so this only
		// fires on misuse.
		return
	}
	ch := c.requests[0]
	c.requests = c.requests[1:]
	select {
	case ch <- pdu:
	default:
	}
}

// isDelivery returns true if the PDU is a subscription delivery.
// It's a cheap byte-level check so we don't parse JSON twice.
func isDelivery(pdu []byte) bool {
	// A delivery always contains `"subscription":`. Command responses
	// never do (even the initial subscribe response uses `"subscribe":`
	// for the sub name, not `"subscription"`).
	const marker = `"subscription"`
	for i := 0; i+len(marker) <= len(pdu); i++ {
		if pdu[i] == '"' && string(pdu[i:i+len(marker)]) == marker {
			return true
		}
	}
	return false
}
