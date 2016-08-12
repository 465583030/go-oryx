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
 This the main entrance of httplb, load-balance for flv/hls+ streaming.
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
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var signature = fmt.Sprintf("HTTPLB/%v", kernel.Version())

// The config object for httplb module.
type HttpLbConfig struct {
	kernel.Config
	Api  string `json:"api"`
	Http struct {
		Listen string `json:"listen"`
	} `json:"http"`
}

func (v *HttpLbConfig) String() string {
	return fmt.Sprintf("%v", &v.Config)
}

func (v *HttpLbConfig) Loads(c string) (err error) {
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
		return fmt.Errorf("Empty api")
	} else if nn := strings.Count(v.Api, "://"); nn != 1 {
		return fmt.Errorf("Api contains %v network", nn)
	}

	if len(v.Http.Listen) == 0 {
		return fmt.Errorf("Empty http listens")
	}
	if nn := strings.Count(v.Http.Listen, "://"); nn != 1 {
		return fmt.Errorf("Listen %v contains %v network", v.Http.Listen, nn)
	}

	return
}

// Create isolate transport for http stream and hls+.
func createHttpTransport() http.RoundTripper {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}

// The virtual connection for hls+
type hlsPlusVirtualConnection struct {
	// for standard player, identify by uuid.
	uuid string
	// for safari or srs player, identify by xpsid if no uuid.
	xpsid string
	// for jwplayer, without uuid/xpsid, identify by tcp connection.
	remoteAddr string
	// each connection use one tcp connection for backend.
	transport  http.RoundTripper
	lastUpdate time.Time
}

func NewHlsPlusVirtualConnection(uuid, xpsid, addr string) *hlsPlusVirtualConnection {
	v := &hlsPlusVirtualConnection{
		uuid: uuid, xpsid: xpsid, remoteAddr: addr,
		lastUpdate: time.Now(),
		transport:  createHttpTransport(),
	}
	return v
}

func (v *hlsPlusVirtualConnection) String() string {
	return fmt.Sprintf("uuid=%v, addr=%v, xpsid=%v", v.uuid, v.remoteAddr, v.xpsid)
}

// The proxyer for hls+
type hlsPlusProxy struct {
	proxy *proxy
	rp    *httputil.ReverseProxy
	lock  *sync.Mutex
	// hls+: virtual connections, key is uuid
	virtualConns map[string]*hlsPlusVirtualConnection
	// hls+: application id for safari or srs player, key is xpsid
	appConns map[string]*hlsPlusVirtualConnection
	// hls+: tcp connections to locate jwplayer, key is removeAddr
	tcpConns map[string]*hlsPlusVirtualConnection
}

func NewHlsPlusProxy(proxy *proxy) *hlsPlusProxy {
	return &hlsPlusProxy{
		proxy:        proxy,
		lock:         &sync.Mutex{},
		virtualConns: make(map[string]*hlsPlusVirtualConnection),
		tcpConns:     make(map[string]*hlsPlusVirtualConnection),
		appConns:     make(map[string]*hlsPlusVirtualConnection),
		rp:           &httputil.ReverseProxy{Director: nil},
	}
}

func (v *hlsPlusProxy) serve(w http.ResponseWriter, r *http.Request) {
	ctx := &kernel.Context{}

	v.lock.Lock()
	defer v.lock.Unlock()

	// idnetify by uuid, then xpsid, then addr(tcp connection).
	var uuid, xpsid, addr string
	q := r.URL.Query()
	uuid = q.Get("shp_uuid")
	if xpsid = q.Get("shp_xpsid"); len(xpsid) == 0 {
		xpsid = r.Header.Get("X-Playback-Session-Id")
	}
	addr = r.RemoteAddr

	// identify virtual connection
	var ok bool
	var vconn *hlsPlusVirtualConnection
	if len(uuid) > 0 && !ok {
		vconn, ok = v.virtualConns[uuid]
	}
	if len(xpsid) > 0 && !ok {
		vconn, ok = v.appConns[uuid]
	}
	if len(addr) > 0 && !ok {
		vconn, ok = v.tcpConns[addr]
	}
	if vconn == nil {
		vconn = NewHlsPlusVirtualConnection(uuid, xpsid, addr)
	}
	vconn.lastUpdate = time.Now()
	//ol.T(ctx, "identify", vconn)

	// update the cache
	if len(uuid) > 0 {
		v.virtualConns[uuid] = vconn
	}
	if len(xpsid) > 0 {
		v.appConns[xpsid] = vconn
	}
	if len(addr) > 0 {
		v.tcpConns[addr] = vconn
	}

	// proxy request.
	v.rp.Director = func(r *http.Request) {
		r.URL.Scheme = "http"

		// to prevent the m3u8 request by proxy.
		//r.Header.Set("X-Hls-Plus", "Proxy")

		r.URL.Host = fmt.Sprintf("127.0.0.1:%v", v.proxy.activePort)
		if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			r.Header.Set("X-Real-IP", ip)
		}
		ol.W(ctx, fmt.Sprintf("proxy hls+ %v to %v", vconn, r.URL.String()))
	}
	v.rp.Transport = vconn.transport

	v.rp.ServeHTTP(w, r)
}

const (
	proxyCleanupInterval  = time.Duration(10) * time.Second
	hlsPlusSessionTimeout = time.Duration(120) * time.Second
)

func (v *hlsPlusProxy) cleanup(ctx ol.Context) {
	defer time.Sleep(proxyCleanupInterval)

	v.lock.Lock()
	defer v.lock.Unlock()

	die := time.Now().Add(-1 * hlsPlusSessionTimeout)

	for _, conn := range v.tcpConns {
		if conn.lastUpdate.After(die) {
			continue
		}

		if len(conn.remoteAddr) > 0 {
			delete(v.tcpConns, conn.remoteAddr)
		}
		if len(conn.xpsid) > 0 {
			delete(v.appConns, conn.xpsid)
		}
		if len(conn.uuid) > 0 {
			delete(v.virtualConns, conn.uuid)
		}

		ol.W(ctx, fmt.Sprintf("remove %v from total=%v/%v/%v",
			conn, len(v.virtualConns), len(v.tcpConns), len(v.appConns)))
	}
}

// The proxy object, serve http stream and hls+.
type proxy struct {
	conf            *HttpLbConfig
	ports           []int
	activePort      int
	httpStreamProxy *httputil.ReverseProxy
	hlsPlus         *hlsPlusProxy
}

func NewProxy(conf *HttpLbConfig) *proxy {
	v := &proxy{
		conf:            conf,
		httpStreamProxy: &httputil.ReverseProxy{Director: nil},
	}
	v.hlsPlus = NewHlsPlusProxy(v)
	return v
}

func (v *proxy) serveHlsPlus(w http.ResponseWriter, r *http.Request) {
	v.hlsPlus.serve(w, r)
}

func (v *proxy) cleanup(ctx ol.Context) {
	v.hlsPlus.cleanup(ctx)
}

func (v *proxy) serveHttpStream(w http.ResponseWriter, r *http.Request) {
	ctx := &kernel.Context{}

	v.httpStreamProxy.Director = func(r *http.Request) {
		r.URL.Scheme = "http"

		r.URL.Host = fmt.Sprintf("127.0.0.1:%v", v.activePort)
		if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			r.Header.Set("X-Real-IP", ip)
		}
		ol.W(ctx, fmt.Sprintf("proxy stream %v to %v", r.RemoteAddr, r.URL.String()))
	}

	if strings.HasSuffix(r.URL.Path, ".xml") {
		// use default for xml requests.
		v.httpStreamProxy.Transport = http.DefaultTransport
	} else {
		// each http stream use isolate transport.
		v.httpStreamProxy.Transport = createHttpTransport()
	}

	v.httpStreamProxy.ServeHTTP(w, r)
}

func (v *proxy) serveHttp(w http.ResponseWriter, r *http.Request) {
	ctx := &kernel.Context{}

	if v.activePort <= 0 {
		oh.Error(ctx, fmt.Errorf("Backend not ready")).ServeHTTP(w, r)
		return
	}

	p := r.URL.Path
	q := r.URL.Query()

	isHlsPlus := strings.HasSuffix(p, ".m3u8")
	if strings.HasSuffix(p, ".ts") && len(q.Get("shp_uuid")) > 0 {
		isHlsPlus = true
	}

	if isHlsPlus {
		v.serveHlsPlus(w, r)
		return
	}

	hasAnySuffixes := func(s string, suffixes ...string) bool {
		for _, suffix := range suffixes {
			if strings.HasSuffix(s, suffix) {
				return true
			}
		}
		return false
	}

	if hasAnySuffixes(p, ".flv", ".ts", ".aac", ".mp3", ".xml") {
		v.serveHttpStream(w, r)
		return
	}
}

const (
	Success       oh.SystemError = 0
	ApiProxyQuery oh.SystemError = 100 + iota
)

func (v *proxy) serveChangeBackendApi(r *http.Request) (string, oh.SystemError) {
	var err error
	q := r.URL.Query()
	ctx := &kernel.Context{}

	var httpPort string
	if httpPort = q.Get("http"); len(httpPort) == 0 {
		return fmt.Sprintf("require query http port"), ApiProxyQuery
	}

	var port int
	if port, err = strconv.Atoi(httpPort); err != nil {
		return fmt.Sprintf("http port is not int, err is %v", err), ApiProxyQuery
	}

	hasProxyed := func(port int) bool {
		for _, p := range v.ports {
			if p == port {
				return true
			}
		}
		return false
	}

	ol.T(ctx, fmt.Sprintf("proxy http to %v, previous=%v, ports=%v", port, v.activePort, v.ports))
	if !hasProxyed(port) {
		v.ports = append(v.ports, port)
	}
	v.activePort = port

	return "", Success
}

func main() {
	var err error
	confFile := oo.ParseArgv("../conf/httplb.json", kernel.Version(), signature)
	fmt.Println("HTTPLB is the load-balance for http flv/hls+ streaming, config is", confFile)

	conf := &HttpLbConfig{}
	if err = conf.Loads(confFile); err != nil {
		ol.E(nil, "Loads config failed, err is", err)
		return
	}
	defer conf.Close()

	ctx := &kernel.Context{}
	ol.T(ctx, fmt.Sprintf("Config ok, %v", conf))

	// httplb is a asprocess of shell.
	asq := make(chan bool, 1)
	oa.WatchNoExit(ctx, oa.Interval, asq)

	var httpListener net.Listener
	addrs := strings.Split(conf.Http.Listen, "://")
	httpNetwork, httpAddr := addrs[0], addrs[1]
	if httpListener, err = net.Listen(httpNetwork, httpAddr); err != nil {
		ol.E(ctx, "http listen failed, err is", err)
		return
	}
	defer httpListener.Close()

	var apiListener net.Listener
	addrs = strings.Split(conf.Api, "://")
	apiNetwork, apiAddr := addrs[0], addrs[1]
	if apiListener, err = net.Listen(apiNetwork, apiAddr); err != nil {
		ol.E(ctx, "http listen failed, err is", err)
		return
	}
	defer apiListener.Close()

	closing := make(chan bool, 1)
	wait := &sync.WaitGroup{}
	proxy := NewProxy(conf)

	// cleanup the proxy.
	go func() {
		ctx := &kernel.Context{}
		for {
			proxy.cleanup(ctx)
		}
	}()

	oh.Server = signature

	// http proxy.
	go func() {
		wait.Add(1)
		defer wait.Done()

		defer func() {
			select {
			case closing <- true:
			default:
			}
		}()

		defer ol.E(ctx, "http proxy ok")

		ol.T(ctx, fmt.Sprintf("handle http://%v/", httpAddr))
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			proxy.serveHttp(w, r)
		})

		server := &http.Server{Addr: httpNetwork, Handler: nil}
		if err = server.Serve(httpListener); err != nil {
			ol.E(ctx, "http serve failed, err is", err)
			return
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

		ol.T(ctx, fmt.Sprintf("handle http://%v/api/v1/proxy?http=8081", apiAddr))
		http.HandleFunc("/api/v1/proxy", func(w http.ResponseWriter, r *http.Request) {
			if msg, err := proxy.serveChangeBackendApi(r); err != Success {
				oh.CplxError(ctx, err, msg).ServeHTTP(w, r)
				return
			}
			oh.Data(ctx, nil).ServeHTTP(w, r)
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
	httpListener.Close()
	apiListener.Close()
	wait.Wait()

	ol.T(ctx, "serve ok")
	return
}
