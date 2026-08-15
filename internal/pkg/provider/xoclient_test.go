// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sourcegraph/jsonrpc2"
	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"
)

// The failure this exists to prevent: the SDK's WebSocket dies, every later
// call returns jsonrpc2.ErrClosed, and the provider fails every reconcile until
// someone restarts the container.
func TestConnectionClosedIsRecognized(t *testing.T) {
	t.Parallel()

	closed := []error{
		jsonrpc2.ErrClosed,
		fmt.Errorf("failed to resolve XO pool: %w", jsonrpc2.ErrClosed),
		errors.New("jsonrpc2: connection is closed"),
		errors.New("use of closed network connection"),
		errors.New("websocket: close 1006 (abnormal closure)"),
		errors.New("unexpected EOF"),
		errors.New("write tcp 10.0.0.1:1234->10.0.0.2:443: broken pipe"),
	}

	for _, err := range closed {
		if !isConnectionClosed(err) {
			t.Errorf("should be treated as a dead connection: %v", err)
		}
	}

	// Real Xen Orchestra rejections must NOT trigger a reconnect: retrying
	// them would double every genuine failure.
	live := []error{
		nil,
		errors.New("jsonrpc2: code 10 message: invalid parameters"),
		errors.New("method not found: VBD.create"),
		xoaclient.NotFound{},
		errors.New("SR_BACKEND_FAILURE_46"),
	}

	for _, err := range live {
		if isConnectionClosed(err) {
			t.Errorf("should not be treated as a dead connection: %v", err)
		}
	}
}

// A dead connection must be replaced and the call retried, without the caller
// seeing an error.
func TestXOCallReconnectsAndRetries(t *testing.T) {
	t.Parallel()

	first := &xoaclient.Client{}
	second := &xoaclient.Client{}

	dials := 0
	x := &xoClient{
		current: first,
		connect: func() (*xoaclient.Client, error) {
			dials++

			return second, nil
		},
	}

	attempts := 0
	got, err := xoCall(x, func(c *xoaclient.Client) (string, error) {
		attempts++
		if c == first {
			return "", jsonrpc2.ErrClosed
		}

		return "recovered", nil
	})
	if err != nil {
		t.Fatalf("expected recovery, got: %v", err)
	}

	if got != "recovered" {
		t.Errorf("result = %q, want %q", got, "recovered")
	}

	if attempts != 2 {
		t.Errorf("operation ran %d times, want 2 (original + retry)", attempts)
	}

	if dials != 1 {
		t.Errorf("reconnected %d times, want 1", dials)
	}

	if x.get() != second {
		t.Error("the dead client is still in place; later calls would keep failing")
	}
}

// A live error must be returned untouched, without reconnecting.
func TestXOCallLeavesRealErrorsAlone(t *testing.T) {
	t.Parallel()

	x := &xoClient{
		current: &xoaclient.Client{},
		connect: func() (*xoaclient.Client, error) {
			t.Error("must not reconnect for an error from Xen Orchestra itself")

			return nil, nil
		},
	}

	want := errors.New("jsonrpc2: code 10 message: invalid parameters")

	attempts := 0
	_, err := xoCall(x, func(*xoaclient.Client) (string, error) {
		attempts++

		return "", want
	})

	if !errors.Is(err, want) {
		t.Errorf("error = %v, want %v", err, want)
	}

	if attempts != 1 {
		t.Errorf("operation ran %d times, want 1 (no retry)", attempts)
	}
}

// If Xen Orchestra is genuinely down, reconnecting fails too. The caller must
// get an error naming both problems rather than a nil client.
func TestXOCallReportsReconnectFailure(t *testing.T) {
	t.Parallel()

	x := &xoClient{
		current: &xoaclient.Client{},
		connect: func() (*xoaclient.Client, error) {
			return nil, errors.New("dial tcp: connection refused")
		},
	}

	_, err := xoCall(x, func(*xoaclient.Client) (string, error) {
		return "", jsonrpc2.ErrClosed
	})
	if err == nil {
		t.Fatal("expected an error when reconnect fails")
	}

	for _, want := range []string{"reconnect failed", "connection refused", "connection is closed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// Concurrent reconciles hitting a dead connection at once must produce one new
// connection, not one per caller.
func TestReconnectIsSharedAcrossCallers(t *testing.T) {
	t.Parallel()

	dead := &xoaclient.Client{}
	fresh := &xoaclient.Client{}

	var mu sync.Mutex
	dials := 0

	x := &xoClient{
		current: dead,
		connect: func() (*xoaclient.Client, error) {
			mu.Lock()
			defer mu.Unlock()
			dials++

			return fresh, nil
		},
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			xoCall(x, func(c *xoaclient.Client) (string, error) { //nolint:errcheck
				if c == dead {
					return "", jsonrpc2.ErrClosed
				}

				return "ok", nil
			})
		}()
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if dials != 1 {
		t.Errorf("opened %d connections, want 1 shared reconnect", dials)
	}
}
