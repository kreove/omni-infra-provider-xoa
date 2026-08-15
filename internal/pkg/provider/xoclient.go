// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"
)

// xoClient keeps a usable Xen Orchestra client available for the lifetime of
// the provider.
//
// The SDK talks JSON-RPC over a single long-lived WebSocket, opened once when
// the client is constructed, and it does not reconnect. Anything that ends that
// socket -- Xen Orchestra restarting or being upgraded, an idle timeout, a
// proxy dropping the connection, a brief network partition -- leaves the client
// permanently broken: every later call returns jsonrpc2.ErrClosed, and the
// provider fails every reconcile until the process is restarted by hand. That
// happened in practice, with the provider failing `ensureTarget` once a minute
// against a perfectly healthy Xen Orchestra.
//
// This wraps the client so a dead connection is replaced and the failed call is
// retried once. Call sites use it exactly like the SDK client.
type xoClient struct {
	mu      sync.RWMutex
	current *xoaclient.Client
	connect func() (*xoaclient.Client, error)
}

// newXOClient dials Xen Orchestra and returns a client that repairs itself.
// connect must return a freshly connected client each time it is called.
func newXOClient(connect func() (*xoaclient.Client, error)) (*xoClient, error) {
	c, err := connect()
	if err != nil {
		return nil, err
	}

	return &xoClient{current: c, connect: connect}, nil
}

func (x *xoClient) get() *xoaclient.Client {
	x.mu.RLock()
	defer x.mu.RUnlock()

	return x.current
}

// reconnect replaces the dead client, unless another caller already did. The
// comparison against stale means a burst of concurrent failures produces one
// new connection rather than one per caller.
func (x *xoClient) reconnect(stale *xoaclient.Client) (*xoaclient.Client, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	if x.current != stale {
		return x.current, nil
	}

	fresh, err := x.connect()
	if err != nil {
		return nil, err
	}

	x.current = fresh

	return fresh, nil
}

// isConnectionClosed reports whether err means the WebSocket is gone, rather
// than Xen Orchestra rejecting the request.
func isConnectionClosed(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, jsonrpc2.ErrClosed) {
		return true
	}

	// The SDK wraps some failures as plain strings, and a socket that dies
	// mid-call surfaces as a websocket or EOF error rather than the sentinel.
	msg := err.Error()
	for _, s := range []string{
		"connection is closed",
		"use of closed network connection",
		"websocket: close",
		"unexpected EOF",
		"broken pipe",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}

	return false
}

// xoCall runs f, reconnecting and retrying once if the connection had died.
func xoCall[T any](x *xoClient, f func(*xoaclient.Client) (T, error)) (T, error) {
	client := x.get()

	result, err := f(client)
	if !isConnectionClosed(err) {
		return result, err
	}

	fresh, rerr := x.reconnect(client)
	if rerr != nil {
		var zero T

		return zero, fmt.Errorf("Xen Orchestra connection lost and reconnect failed: %w (original error: %v)", rerr, err)
	}

	return f(fresh)
}

// xoDo is xoCall for operations that only return an error.
func xoDo(x *xoClient, f func(*xoaclient.Client) error) error {
	_, err := xoCall(x, func(c *xoaclient.Client) (struct{}, error) {
		return struct{}{}, f(c)
	})

	return err
}

// The methods below mirror the SDK surface this provider uses, so the
// reconnecting client is a drop-in replacement at every call site.

func (x *xoClient) GetPools(req xoaclient.Pool) ([]xoaclient.Pool, error) {
	return xoCall(x, func(c *xoaclient.Client) ([]xoaclient.Pool, error) { return c.GetPools(req) })
}

func (x *xoClient) GetStorageRepositoryById(id string) (xoaclient.StorageRepository, error) {
	return xoCall(x, func(c *xoaclient.Client) (xoaclient.StorageRepository, error) {
		return c.GetStorageRepositoryById(id)
	})
}

func (x *xoClient) GetNetwork(req xoaclient.Network) (*xoaclient.Network, error) {
	return xoCall(x, func(c *xoaclient.Client) (*xoaclient.Network, error) { return c.GetNetwork(req) })
}

func (x *xoClient) GetTemplate(req xoaclient.Template) ([]xoaclient.Template, error) {
	return xoCall(x, func(c *xoaclient.Client) ([]xoaclient.Template, error) { return c.GetTemplate(req) })
}

func (x *xoClient) CreateVDI(req xoaclient.CreateVDIReq) (xoaclient.VDI, error) {
	return xoCall(x, func(c *xoaclient.Client) (xoaclient.VDI, error) { return c.CreateVDI(req) })
}

func (x *xoClient) CreateVm(req xoaclient.Vm, timeout time.Duration) (*xoaclient.Vm, error) {
	return xoCall(x, func(c *xoaclient.Client) (*xoaclient.Vm, error) { return c.CreateVm(req, timeout) })
}

func (x *xoClient) GetVms(req xoaclient.Vm) ([]xoaclient.Vm, error) {
	return xoCall(x, func(c *xoaclient.Client) ([]xoaclient.Vm, error) { return c.GetVms(req) })
}

func (x *xoClient) GetVm(req xoaclient.Vm) (*xoaclient.Vm, error) {
	return xoCall(x, func(c *xoaclient.Client) (*xoaclient.Vm, error) { return c.GetVm(req) })
}

func (x *xoClient) GetDisks(vm *xoaclient.Vm) ([]xoaclient.Disk, error) {
	return xoCall(x, func(c *xoaclient.Client) ([]xoaclient.Disk, error) { return c.GetDisks(vm) })
}

func (x *xoClient) GetVIFs(vm *xoaclient.Vm) ([]xoaclient.VIF, error) {
	return xoCall(x, func(c *xoaclient.Client) ([]xoaclient.VIF, error) { return c.GetVIFs(vm) })
}

func (x *xoClient) DeleteDisk(vm xoaclient.Vm, disk xoaclient.Disk) error {
	return xoDo(x, func(c *xoaclient.Client) error { return c.DeleteDisk(vm, disk) })
}

func (x *xoClient) DeleteVm(id string) error {
	return xoDo(x, func(c *xoaclient.Client) error { return c.DeleteVm(id) })
}

func (x *xoClient) StartVm(id string) error {
	return xoDo(x, func(c *xoaclient.Client) error { return c.StartVm(id) })
}

func (x *xoClient) Call(method string, params, result interface{}) error {
	return xoDo(x, func(c *xoaclient.Client) error { return c.Call(method, params, result) })
}
