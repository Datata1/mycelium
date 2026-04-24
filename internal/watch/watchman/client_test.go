package watchman

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// TestClient_Send_Response exercises the one-request-one-response
// pattern over a net.Pipe pair, without talking to a real watchman.
func TestClient_Send_Response(t *testing.T) {
	client, server := net.Pipe()
	c := newClient(client)
	t.Cleanup(func() { _ = c.Close() })

	// Server goroutine: read the request, reply with a fixed PDU.
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(server)
		if !sc.Scan() {
			t.Errorf("server: read request: %v", sc.Err())
			return
		}
		var req []any
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			t.Errorf("server: parse request: %v", err)
			return
		}
		if got := req[0]; got != "watch-project" {
			t.Errorf("server: cmd = %v, want watch-project", got)
		}
		resp := `{"watch":"/tmp/repo","relative_path":""}` + "\n"
		if _, err := io.WriteString(server, resp); err != nil {
			t.Errorf("server: write reply: %v", err)
		}
	}()

	raw, err := c.Send([]any{"watch-project", "/tmp/repo"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	var reply struct {
		Watch string `json:"watch"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if reply.Watch != "/tmp/repo" {
		t.Errorf("watch = %q, want /tmp/repo", reply.Watch)
	}
	<-done
}

// TestClient_Delivery_Routing confirms subscription deliveries go to
// Deliveries() while command responses go to Send()'s reply channel,
// even when they interleave on the wire.
func TestClient_Delivery_Routing(t *testing.T) {
	client, server := net.Pipe()
	c := newClient(client)
	t.Cleanup(func() { _ = c.Close() })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(server)
		// Read one request.
		if !sc.Scan() {
			t.Errorf("server: read: %v", sc.Err())
			return
		}
		// Send a delivery *first*, then the reply. The client must
		// route them to different consumers.
		delivery := `{"subscription":"s1","files":[{"name":"a.go","exists":true,"new":true}]}` + "\n"
		reply := `{"subscribe":"s1","clock":"c:123"}` + "\n"
		if _, err := io.WriteString(server, delivery); err != nil {
			t.Errorf("server: write delivery: %v", err)
			return
		}
		if _, err := io.WriteString(server, reply); err != nil {
			t.Errorf("server: write reply: %v", err)
			return
		}
	}()

	// Kick off the Send in a goroutine so we can also consume the
	// delivery; both arrive on separate pathways.
	replyCh := make(chan RawPDU, 1)
	errCh := make(chan error, 1)
	go func() {
		pdu, err := c.Send([]any{"subscribe", "/tmp/repo", "s1", map[string]any{}})
		if err != nil {
			errCh <- err
			return
		}
		replyCh <- pdu
	}()

	select {
	case d := <-c.Deliveries():
		var env struct {
			Subscription string `json:"subscription"`
		}
		if err := json.Unmarshal(d, &env); err != nil {
			t.Fatalf("parse delivery: %v", err)
		}
		if env.Subscription != "s1" {
			t.Errorf("delivery subscription = %q, want s1", env.Subscription)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery did not arrive within 1s")
	}

	select {
	case pdu := <-replyCh:
		var env struct {
			Subscribe string `json:"subscribe"`
		}
		if err := json.Unmarshal(pdu, &env); err != nil {
			t.Fatalf("parse reply: %v", err)
		}
		if env.Subscribe != "s1" {
			t.Errorf("reply subscribe = %q, want s1", env.Subscribe)
		}
	case err := <-errCh:
		t.Fatalf("send: %v", err)
	case <-time.After(time.Second):
		t.Fatal("reply did not arrive within 1s")
	}

	wg.Wait()
}

// TestClient_ReadError surfaces on connection close.
func TestClient_ReadError(t *testing.T) {
	client, server := net.Pipe()
	c := newClient(client)
	t.Cleanup(func() { _ = c.Close() })

	_ = server.Close()

	select {
	case err := <-c.ReadErrors():
		if err == nil {
			t.Error("expected a non-nil read error")
		}
	case <-time.After(time.Second):
		t.Fatal("read error not reported within 1s")
	}
}
