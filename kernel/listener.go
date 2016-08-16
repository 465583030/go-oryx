/*
The MIT License (MIT)

Copyright (c) 2016 winlin

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/

/*
 This is the listeners for oryx.
*/
package kernel

import (
	"fmt"
	ol "github.com/ossrs/go-oryx-lib/logger"
	"net"
	"strings"
	"sync"
)

// The listener is disposed by user.
var ListenerDisposed error = fmt.Errorf("listener disposed")

// The tcp listeners which support reload.
// @remark listener will return error ListenerDisposed when reuse a disposed listener.
type TcpListeners struct {
	// The config and listener objects.
	addrs     []string
	listeners []*net.TCPListener
	// Used to get the connection or error for accept.
	conns  chan *net.TCPConn
	errors chan error
	// Used to ensure all gorutine quit.
	wait *sync.WaitGroup
	// Used to notify all goroutines to quit.
	closing chan bool
	// Used to prevent reuse this object.
	disposed    bool
	disposeLock *sync.Mutex
}

// Listen at addrs format as netowrk://laddr, for example,
// tcp://:1935, tcp4://:1935, tcp6://1935, tcp://0.0.0.0:1935
func NewTcpListeners(addrs []string) (v *TcpListeners, err error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no listens")
	}

	for _, v := range addrs {
		if !strings.HasPrefix(v, "tcp://") && !strings.HasPrefix(v, "tcp4://") && !strings.HasPrefix(v, "tcp6://") {
			return nil, fmt.Errorf("%v should prefix with tcp://, tcp4:// or tcp6://", v)
		}
		if n := strings.Count(v, "://"); n != 1 {
			return nil, fmt.Errorf("%v contains %d network identify", v, n)
		}
	}

	v = &TcpListeners{
		addrs:       addrs,
		conns:       make(chan *net.TCPConn),
		errors:      make(chan error),
		wait:        &sync.WaitGroup{},
		closing:     make(chan bool, 1),
		disposeLock: &sync.Mutex{},
	}

	return
}

// @remark error ListenerDisposed when listener is disposed.
func (v *TcpListeners) ListenTCP() (err error) {
	// user should never listen on a disposed listener
	v.disposeLock.Lock()
	defer v.disposeLock.Unlock()
	if v.disposed {
		return ListenerDisposed
	}

	for _, addr := range v.addrs {
		var network, laddr string
		if vs := strings.Split(addr, "://"); true {
			network, laddr = vs[0], vs[1]
		}

		var l net.Listener
		if l, err = net.Listen(network, laddr); err != nil {
			return
		} else if l, ok := l.(*net.TCPListener); !ok {
			panic("listener: must be *net.TCPListener")
		} else {
			v.listeners = append(v.listeners, l)
		}
	}

	for i, l := range v.listeners {
		go v.acceptFrom(l, v.addrs[i])
	}

	return
}

func (v *TcpListeners) acceptFrom(l *net.TCPListener, addr string) {
	v.wait.Add(1)
	defer v.wait.Done()

	ctx := &Context{}

	for {
		if err := v.doAcceptFrom(ctx, l); err != nil {
			if err != ListenerDisposed {
				ol.W(ctx, "listener:", addr, "quit, err is", err)
			}
			return
		}
	}

	return
}

func (v *TcpListeners) doAcceptFrom(ctx ol.Context, l *net.TCPListener) (err error) {
	defer func() {
		if err != nil && err != ListenerDisposed {
			select {
			case v.errors <- err:
			case c := <-v.closing:
				v.closing <- c
			}
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			if err != nil {
				ol.E(ctx, "listener: recover from", r, "and err is", err)
				return
			}

			if r, ok := r.(error); ok {
				err = r
			} else {
				err = fmt.Errorf("system error", r)
			}
			ol.E(ctx, "listener: recover from", err)
		}
	}()

	var conn *net.TCPConn
	if conn, err = l.AcceptTCP(); err != nil {
		// when disposed, ignore any error for it's user closed listener.
		if v.disposed {
			err = ListenerDisposed
			return
		}

		ol.E(ctx, "listener: accept failed, err is", err)
		return
	}

	select {
	case v.conns <- conn:
	case c := <-v.closing:
		v.closing <- c

		// we got a connection but not accept by user and listener is closed,
		// we must close this connection for user never get it.
		conn.Close()
		ol.W(ctx, "listener: drop connection", conn.RemoteAddr())
	}

	return
}

// @remark error ListenerDisposed when listener is disposed.
func (v *TcpListeners) AcceptTCP() (c *net.TCPConn, err error) {
	// should never lock, for it's wait goroutine.
	if err = func() error {
		// user should close a disposed listener.
		v.disposeLock.Lock()
		defer v.disposeLock.Unlock()
		if v.disposed {
			return ListenerDisposed
		}
		return nil
	}(); err != nil {
		return
	}

	var ok bool
	select {
	case c, ok = <-v.conns:
	case err, ok = <-v.errors:
	case c := <-v.closing:
		v.closing <- c
		return nil, ListenerDisposed
	}

	// when chan closed, the listener is disposed.
	if !ok {
		return nil, ListenerDisposed
	}
	return
}

// io.Closer
// @remark error ListenerDisposed when listener is disposed.
func (v *TcpListeners) Close() (err error) {
	// unblock all listener and user goroutines
	select {
	case v.closing <- true:
	default:
	}

	// user should close a disposed listener.
	v.disposeLock.Lock()
	defer v.disposeLock.Unlock()
	if v.disposed {
		return ListenerDisposed
	}
	// set to disposed to prevent reuse this object.
	v.disposed = true

	// interrupt all listeners.
	for _, v := range v.listeners {
		if r := v.Close(); r != nil {
			err = r
		}
	}

	// wait for all listener internal goroutines to quit.
	v.wait.Wait()

	// close channels to unblock the user goroutine to AcceptTCP()
	close(v.conns)
	close(v.errors)

	return
}
