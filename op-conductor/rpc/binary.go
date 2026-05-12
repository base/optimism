package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ethereum/go-ethereum/log"
)

// CommitUnsafePayloadPath is the HTTP route for the SSZ binary commit endpoint.
// External clients (op-node, base's Rust CL replacement, etc.) POST a raw
// SSZ-encoded ExecutionPayloadEnvelope here. The body is handed verbatim to
// raft.Apply; the FSM validates by attempting UnmarshalSSZ on receive.
//
// Wire format:
//   - method:        POST
//   - path:          /commit-unsafe-payload
//   - content-type:  application/octet-stream
//   - body:          SSZ-encoded ExecutionPayloadEnvelope (no length prefix,
//                    body length implies SSZ scope; current FSM tries V4 then
//                    V3, matching the JSON-RPC path).
//   - response:      200 on success, 4xx for client errors, 5xx for raft
//                    errors. Body is empty on 200, plain-text error message
//                    otherwise.
const CommitUnsafePayloadPath = "/commit-unsafe-payload"

// SSZContentType is the content type clients should send for the binary endpoint.
const SSZContentType = "application/octet-stream"

// commitSSZBackend is the subset of the conductor backend the binary endpoint needs.
type commitSSZBackend interface {
	CommitUnsafePayloadSSZ(ctx context.Context, ssz []byte) error
}

// BinaryCommitHandler returns an http.Handler that accepts SSZ-encoded payloads
// and forwards them to the conductor's raft layer. maxBodyBytes caps the
// request body to prevent DoS; 0 means no cap (not recommended).
func BinaryCommitHandler(lgr log.Logger, backend commitSSZBackend, maxBodyBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" && ct != SSZContentType {
			http.Error(w, fmt.Sprintf("unsupported content-type %q, want %s", ct, SSZContentType), http.StatusUnsupportedMediaType)
			return
		}

		body := r.Body
		if maxBodyBytes > 0 {
			// Reject upfront if Content-Length declares an over-limit body.
			if r.ContentLength > maxBodyBytes {
				http.Error(w, fmt.Sprintf("payload too large: %d > %d", r.ContentLength, maxBodyBytes), http.StatusRequestEntityTooLarge)
				return
			}
			body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}

		// When Content-Length is set, pre-allocate the exact buffer and use
		// ReadFull. Avoids io.ReadAll's grow-and-copy. ~10% faster end-to-end
		// for multi-MB bodies; pure win when the client sends Content-Length
		// (every standard HTTP client does).
		var ssz []byte
		var err error
		if r.ContentLength > 0 {
			ssz = make([]byte, r.ContentLength)
			_, err = io.ReadFull(body, ssz)
		} else {
			ssz, err = io.ReadAll(body)
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, fmt.Sprintf("payload too large: > %d bytes", maxErr.Limit), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
			return
		}
		if len(ssz) == 0 {
			http.Error(w, "empty payload", http.StatusBadRequest)
			return
		}

		if err := backend.CommitUnsafePayloadSSZ(r.Context(), ssz); err != nil {
			lgr.Warn("failed to commit unsafe payload (binary)", "err", err, "size", len(ssz))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
