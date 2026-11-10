package transport

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// CloseIdleConnections closes the connection a request left open, rather
// than leaving it for IdleStreamTimeout.
//
// It is what the node's return poster calls on the client it is done
// with, and a no-op here would look the same from the node's side as a
// working one: the return after a renewal goes out on a new connection
// either way. The difference is only on the hub, which went on holding
// the old connection for 90 s, so this watches the server.
func TestCloseIdleConnectionsClosesTheConnection(t *testing.T) {
	f := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	pair, _ := f.enrolledPair(t, "web1.example")
	client := &Client{HubURL: f.url, CA: f.ca.Cert, Cert: &pair, ServerName: "localhost", Timeout: 5 * time.Second}
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.closed:
		t.Fatal("the connection closed before anything asked it to; this test cannot tell")
	default:
	}
	client.CloseIdleConnections()
	select {
	case <-f.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the server still holds the client's connection 5 s after CloseIdleConnections")
	}
}

// A client that has made no request has nothing to close, and saying so
// does not build a transport to close it on.
func TestCloseIdleConnectionsBeforeAnyRequest(t *testing.T) {
	c := &Client{HubURL: "https://127.0.0.1:1"}
	c.CloseIdleConnections()
	if c.http != nil {
		t.Error("CloseIdleConnections built an HTTP client")
	}
}
