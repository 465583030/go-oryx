// The MIT License (MIT)
//
// Copyright (c) 2013-2015 Oryx(ossrs)
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of
// this software and associated documentation files (the "Software"), to deal in
// the Software without restriction, including without limitation the rights to
// use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
// the Software, and to permit persons to whom the Software is furnished to do so,
// subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
// FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
// COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
// IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
// CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package agent

import (
	"fmt"
	"github.com/ossrs/go-oryx/core"
	"github.com/ossrs/go-oryx/protocol"
	"net"
	"runtime/debug"
)

// the rtmp publish or play agent,
// to listen at RTMP(tcp://1935) and recv data from RTMP publisher or player,
// when identified the client type, redirect to the specified agent.
type Rtmp struct {
	endpoint string
	wc       core.WorkerContainer
	l        net.Listener
}

func NewRtmp(wc core.WorkerContainer) (agent core.OpenCloser) {
	v := &Rtmp{
		wc: wc,
	}

	core.Conf.Subscribe(v)

	return v
}

// interface core.Agent
func (v *Rtmp) Open() (err error) {
	return v.applyListen(core.Conf)
}

func (v *Rtmp) Close() (err error) {
	core.Conf.Unsubscribe(v)
	return v.close()
}

func (v *Rtmp) close() (err error) {
	if v.l == nil {
		return
	}

	if err = v.l.Close(); err != nil {
		core.Error.Println("close rtmp listener failed. err is", err)
		return
	}
	v.l = nil

	core.Trace.Println("close rtmp listen", v.endpoint, "ok")
	return
}

func (v *Rtmp) applyListen(c *core.Config) (err error) {
	v.endpoint = fmt.Sprintf(":%v", c.Listen)

	ep := v.endpoint
	if v.l, err = net.Listen("tcp", ep); err != nil {
		core.Error.Println("rtmp listen at", ep, "failed. err is", err)
		return
	}
	core.Trace.Println("rtmp listen at", ep)

	// accept cycle
	v.wc.GFork("", func(wc core.WorkerContainer) {
		for v.l != nil {
			var c net.Conn
			if c, err = v.l.Accept(); err != nil {
				if v.l != nil {
					core.Warn.Println("accept failed. err is", err)
				}
				return
			}

			// use gfork to serve the connection.
			v.wc.GFork("", func(wc core.WorkerContainer) {
				defer func() {
					if r := recover(); r != nil {
						if !core.IsNormalQuit(r) {
							core.Warn.Println("rtmp ignore", r)
						}

						core.Error.Println(string(debug.Stack()))
					}
				}()

				conn, err := v.identify(c)
				defer conn.Close()

				if !core.IsNormalQuit(err) {
					core.Warn.Println("ignore error when identify rtmp. err is", err)
					return
				}
				core.Info.Println("rtmp identify ok.")
			})
		}
	})

	// should quit?
	v.wc.GFork("", func(wc core.WorkerContainer) {
		<-wc.QC()
		_ = v.close()
		wc.Quit()
	})

	return
}

func (v *Rtmp) identify(c net.Conn) (conn *protocol.RtmpConnection, err error) {
	conn = protocol.NewRtmpConnection(c, v.wc)

	core.Trace.Println("rtmp accept", c.RemoteAddr())

	if err = conn.Handshake(); err != nil {
		core.Error.Println("rtmp handshake failed. err is", err)
		return
	}
	core.Info.Println("rtmp handshake ok.")

	var r *protocol.RtmpRequest
	if r, err = conn.ConnectApp(); err != nil {
		if !core.IsNormalQuit(err) {
			core.Error.Println("rtmp connnect app failed. err is", err)
		}
		return
	}
	core.Info.Println("rtmp connect app ok, tcUrl is", r.TcUrl)

	// TODO: FIXME: should set the TCP_NODELAY to false.
	return
}

// interface ReloadHandler
func (v *Rtmp) OnReloadGlobal(scope int, cc, pc *core.Config) (err error) {
	if scope != core.ReloadListen {
		return
	}

	if err = v.close(); err != nil {
		return
	}

	if err = v.applyListen(cc); err != nil {
		return
	}

	return
}
