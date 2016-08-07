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
	oj "github.com/ossrs/go-oryx-lib/json"
	ol "github.com/ossrs/go-oryx-lib/logger"
	oo "github.com/ossrs/go-oryx-lib/options"
	"github.com/ossrs/go-oryx/kernel"
	"net"
	"os"
	"time"
)

var signature = fmt.Sprintf("RTMPLB/%v", kernel.Version())

// The config object for rtmplb module.
type RtmpLbConfig struct {
	kernel.Config
	Rtmp struct {
		Listens  []string `json:"listens"`
		Backends []string `json:"backends"`
	} `json:"rtmp"`
}

func (v *RtmpLbConfig) String() string {
	return fmt.Sprintf("%v, rtmp(listen=%v,backends=%v)", &v.Config, v.Rtmp.Listens, v.Rtmp.Backends)
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

	if r := &v.Rtmp; len(r.Backends) == 0 {
		return fmt.Errorf("no backends")
	} else if len(r.Listens) == 0 {
		return fmt.Errorf("no listens")
	}

	return
}

func main() {
	var err error
	confFile := oo.ParseArgv("conf/rtmplb.json", kernel.Version(), signature)
	fmt.Println("RTMPLB is the load-balance for rtmp streaming, config is", confFile)

	conf := &RtmpLbConfig{}
	if err = conf.Loads(confFile); err != nil {
		ol.E(nil, "Loads config failed, err is", err)
		return
	}
	defer conf.Close()

	ctx := &kernel.Context{}
	ol.T(ctx, fmt.Sprintf("Config ok, %v", conf))

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

	// serve clients.
	go func() {
		time.Sleep(time.Duration(3) * time.Second)
		ol.T(ctx, "close listener")
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

		_ = c
	}

	go func() {
		f := func() {
			var f *os.File
			if f, err = os.OpenFile("test.id", os.O_WRONLY|os.O_CREATE, 0644); err != nil {
				ol.E(nil, "open id failed, err is", err)
				return
			}
			defer f.Close()

			ol.T(nil, "write id ok")
			f.Write([]byte(fmt.Sprintf("%v", time.Now().String())))
		}
		for {
			f()
			time.Sleep(time.Duration(3) * time.Second)
		}

	}()

	for {
		ol.E(ctx, "process ok")
		time.Sleep(time.Duration(3) * time.Microsecond)
	}
	ol.T(ctx, "serve ok")

	return
}
