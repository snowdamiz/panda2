package billing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPSolanaRPCClientChecksStatusBeforeTransactionFetch(t *testing.T) {
	signature := encodeBase58(bytes.Repeat([]byte{1}, 64))
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		methods = append(methods, request.Method)
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "getSignatureStatuses":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"value":[{"confirmationStatus":"finalized","err":null}]},"id":1}`))
		case "getTransaction":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"slot":1,"meta":{"err":null},"transaction":{"message":{"accountKeys":[],"instructions":[]},"signatures":[]}},"id":1}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := NewHTTPSolanaRPCClient(server.URL)
	status, err := client.GetSignatureStatus(context.Background(), signature)
	if err != nil || status.ConfirmationStatus != "finalized" {
		t.Fatalf("GetSignatureStatus: status=%+v err=%v", status, err)
	}
	if _, err := client.GetTransaction(context.Background(), signature, "finalized"); err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if len(methods) != 2 || methods[0] != "getSignatureStatuses" || methods[1] != "getTransaction" {
		t.Fatalf("unexpected RPC method sequence: %v", methods)
	}
}

func TestHTTPSolanaRPCClientRejectsInvalidPayloadsBeforeRPC(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewHTTPSolanaRPCClient(server.URL)
	if _, err := client.GetSignatureStatus(context.Background(), "not-a-signature"); err == nil {
		t.Fatal("expected malformed signature to be rejected")
	}
	oversized := base64.StdEncoding.EncodeToString(make([]byte, maxSolanaTransactionBytes+1))
	if _, err := client.SendTransaction(context.Background(), oversized, "finalized"); err == nil {
		t.Fatal("expected oversized transaction to be rejected")
	}
	if requests != 0 {
		t.Fatalf("invalid requests should not reach the RPC provider, got %d", requests)
	}
}

func TestConfirmationAtLeast(t *testing.T) {
	tests := []struct {
		actual   string
		required string
		want     bool
	}{
		{actual: "finalized", required: "confirmed", want: true},
		{actual: "confirmed", required: "confirmed", want: true},
		{actual: "confirmed", required: "finalized", want: false},
		{actual: "processed", required: "confirmed", want: false},
		{actual: "", required: "finalized", want: false},
	}
	for _, test := range tests {
		if got := confirmationAtLeast(test.actual, test.required); got != test.want {
			t.Errorf("confirmationAtLeast(%q, %q) = %v, want %v", test.actual, test.required, got, test.want)
		}
	}
}
