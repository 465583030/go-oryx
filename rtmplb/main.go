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
 This the main entrance of rtmplb, load-balance for rtmp streaming.
*/
package main

import (
	"encoding/json"
	"fmt"
	oa "github.com/ossrs/go-oryx-lib/asprocess"
	oh "github.com/ossrs/go-oryx-lib/http"
	oj "github.com/ossrs/go-oryx-lib/json"
	ol "github.com/ossrs/go-oryx-lib/logger"
	oo "github.com/ossrs/go-oryx-lib/options"
	"github.com/ossrs/go-oryx/kernel"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var signature = fmt.Sprintf("RTMPLB/%v", kernel.Version())

// The config object for rtmplb module.
type RtmpLbConfig struct {
	kernel.Config
	Api  string `json:"api"`
	Rtmp struct {
		Listens []string `json:"listens"`
	} `json:"rtmp"`
}

func (v *RtmpLbConfig) String() string {
	return fmt.Sprintf("%v, api=%v, rtmp(listen=%v)", &v.Config, v.Api, v.Rtmp.Listens)
}

func (v *RtmpLbConfig) Loads(c string) (err error) {
	var f *os.File
	if f, err = os.Open(c); err != nil {
		ol.E(nil, "Open config failed, err is", err)
		return
	}
	defer f.Close()

	r := json.NewDecoder(oj.NewJsonPlusReader(f))
	if err = r.Decode(v); err != nil {
		ol.E(nil, "Decode config failed, err is", err)
		return
	}

	if err = v.Config.OpenLogger(); err != nil {
		ol.E(nil, "Open logger failed, err is", err)
		return
	}

	if len(v.Api) == 0 {
		return fmt.Errorf("no api")
	}

	if r := &v.Rtmp; len(r.Listens) == 0 {
		return fmt.Errorf("no rtmp listens")
	}

	return
}

type proxy struct {
	conf       *RtmpLbConfig
	ctx        ol.Context
	ports      []int
	activePort int
}

func NewProxy(conf *RtmpLbConfig) *proxy {
	return &proxy{conf: conf, ctx: &kernel.Context{}}
}

const (
	// when backend connect error, retry interval.
	RetryBackend = time.Duration(3) * time.Second
	// when backend connect error, retry max count.
	RetryMax = 3
)

func (v *proxy) serveRtmp(client *net.TCPConn) (err error) {
	ctx := v.ctx

	defer func() {
		if r := recover(); r != nil {
			if err == nil {
				err = fmt.Errorf("panic %v", r)
				ol.W(ctx, "ignore panic, err is", err)
			} else {
				ol.W(ctx, fmt.Sprintf("ignore panic %v, err is %v", r, err))
			}
		}
	}()
	defer client.Close()

	// connect to backend.
	var backend *net.TCPConn
	connectBackend := func() error {
		defer func() {
			if backend == nil {
				time.Sleep(RetryBackend)
			}
		}()

		if v.activePort <= 0 {
			return fmt.Errorf("ignore no backend, port=%v, ports=%v", v.activePort, v.ports)
		}

		addr := fmt.Sprintf("127.0.0.1:%v", v.activePort)
		if c, err := net.DialTimeout("tcp", addr, RetryBackend); err != nil {
			ol.W(ctx, "connect backend", addr, "failed, err is", err)
			return err
		} else {
			backend = c.(*net.TCPConn)
		}

		return nil
	}
	for i := 0; i < RetryMax && backend == nil; i++ {
		if r := connectBackend(); err == nil {
			err = r
		}
	}
	if backend == nil {
		ol.W(ctx, "proxy failed for no backend, err is", err)
		return
	}
	defer backend.Close()
	ol.T(ctx, "proxy", client.RemoteAddr(), "to", backend.RemoteAddr())

	// proxy c to conn
	var disposed bool
	closing := make(chan bool, 1)
	wait := &sync.WaitGroup{}
	var nr, nw int64
	go func() {
		wait.Add(1)
		defer wait.Done()

		defer func() {
			select {
			case closing <- true:
			default:
			}
		}()

		if nw, err = io.Copy(client, backend); err != nil {
			if !disposed {
				ol.E(ctx, fmt.Sprintf("proxy rtmp<=backend failed, nn=%v, err is %v", nw, err))
			}
			return
		}
	}()
	go func() {
		wait.Add(1)
		defer wait.Done()

		defer func() {
			select {
			case closing <- true:
			default:
			}
		}()

		if nr, err = io.Copy(backend, client); err != nil {
			if !disposed {
				ol.E(ctx, fmt.Sprintf("proxy rtmp=>backend failed, nn=%v, err is %v", nr, err))
			}
			return
		}
	}()

	disposed = true
	<-closing
	closing <- true
	wait.Wait()
	ol.T(ctx, fmt.Sprintf("proxy client ok, read=%v, write=%v", nr, nw))

	return
}

const (
	// error when api proxy parse parameters.
	ApiProxyQuery oh.SystemError = 100 + iota
)

func (v *proxy) serveApi(w http.ResponseWriter, r *http.Request) {
	var err error
	q := r.URL.Query()
	ctx := v.ctx

	var rtmp string
	if rtmp = q.Get("rtmp"); len(rtmp) == 0 {
		msg := fmt.Sprintf("require query rtmp port")
		oh.CplxError(ctx, ApiProxyQuery, msg).ServeHTTP(w, r)
		return
	}

	var port int
	if port, err = strconv.Atoi(rtmp); err != nil {
		msg := fmt.Sprintf("rtmp port is not int, err is %v", err)
		oh.CplxError(ctx, ApiProxyQuery, msg).ServeHTTP(w, r)
		return
	}

	ol.T(ctx, fmt.Sprintf("proxy rtmp to %v, previous=%v, ports=%v", port, v.activePort, v.ports))
	if !v.hasProxyed(port) {
		v.ports = append(v.ports, port)
	}
	v.activePort = port

	oh.Data(v.ctx, nil).ServeHTTP(w, r)
}

func (v *proxy) hasProxyed(port int) bool {
	for _, p := range v.ports {
		if p == port {
			return true
		}
	}
	return false
}

func main() {
	var err error
	confFile := oo.ParseArgv("../conf/rtmplb.json", kernel.Version(), signature)
	fmt.Println("RTMPLB is the load-balance for rtmp streaming, config is", confFile)

	conf := &RtmpLbConfig{}
	if err = conf.Loads(confFile); err != nil {
		ol.E(nil, "Loads config failed, err is", err)
		return
	}
	defer conf.Close()

	ctx := &kernel.Context{}
	ol.T(ctx, fmt.Sprintf("Config ok, %v", conf))

	// rtmplb is a asprocess of shell.
	asq := make(chan bool, 1)
	oa.WatchNoExit(ctx, oa.Interval, asq)

	var listener *kernel.TcpListeners
	if listener, err = kernel.NewTcpListeners(conf.Rtmp.Listens); err != nil {
		ol.E(ctx, "create listener failed, err is", err)
		return
	}
	defer listener.Close()

	if err = listener.ListenTCP(); err != nil {
		ol.E(ctx, "listen tcp failed, err is", err)
		return
	}

	var apiListener net.Listener
	apiAddr := strings.Split(conf.Api, "://")[1]
	if apiListener, err = net.Listen("tcp", apiAddr); err != nil {
		ol.E(ctx, "http listen failed, err is", err)
		return
	}
	defer apiListener.Close()

	closing := make(chan bool, 1)
	wait := &sync.WaitGroup{}
	proxy := NewProxy(conf)

	// rtmp connections
	go func() {
		wait.Add(1)
		defer wait.Done()

		defer func() {
			select {
			case closing <- true:
			default:
			}
		}()

		defer ol.E(ctx, "rtmp accepter ok")

		defer func() {
			listener.Close()
		}()

		for {
			var c *net.TCPConn
			if c, err = listener.AcceptTCP(); err != nil {
				if err != kernel.ListenerDisposed {
					ol.E(ctx, "accept failed, err is", err)
				}
				break
			}

			//ol.T(ctx, "got rtmp client", c.RemoteAddr())
			go proxy.serveRtmp(c)
		}
	}()

	// control messages
	go func() {
		wait.Add(1)
		defer wait.Done()

		defer func() {
			select {
			case closing <- true:
			default:
			}
		}()

		defer ol.E(ctx, "http handler ok")

		oh.Server = signature

		ol.T(ctx, fmt.Sprintf("handle http://%v/api/v1/version", apiAddr))
		http.HandleFunc("/api/v1/version", func(w http.ResponseWriter, r *http.Request) {
			oh.WriteVersion(w, r, kernel.Version())
		})

		ol.T(ctx, fmt.Sprintf("handle http://%v/api/v1/proxy?rtmp=19350", apiAddr))
		http.HandleFunc("/api/v1/proxy", func(w http.ResponseWriter, r *http.Request) {
			proxy.serveApi(w, r)
		})

		server := &http.Server{Addr: apiAddr, Handler: nil}
		if err = server.Serve(apiListener); err != nil {
			ol.E(ctx, "http serve failed, err is", err)
			return
		}
	}()

	// listen singal.
	go func() {
		ss := make(chan os.Signal)
		signal.Notify(ss, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
		for s := range ss {
			ol.E(ctx, "quit for signal", s)
			closing <- true
		}
	}()

	// cleanup when got closing event.
	select {
	case <-closing:
		closing <- true
	case <-asq:
	}
	listener.Close()
	apiListener.Close()
	wait.Wait()

	ol.T(ctx, "serve ok")
	return
}
