package pairing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPairing404NamesTheURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := PostCreate(context.Background(), srv.URL, CreateRequest{Key: "k", Agent: "box"})
	if err == nil {
		t.Fatal("expected 404")
	}
	msg := err.Error()
	if !strings.Contains(msg, srv.URL+"/pair") || !strings.Contains(msg, "404") || !strings.Contains(msg, "address may be wrong") {
		t.Fatal(msg)
	}
}
