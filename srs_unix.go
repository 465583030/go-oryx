// The MIT License (MIT)
//
// Copyright (c) 2013-2015 SRS(ossrs)
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

// +build darwin dragonfly freebsd nacl netbsd openbsd solaris linux

package main

import (
	"github.com/ossrs/go-daemon"
	"github.com/ossrs/go-srs/app"
	"github.com/ossrs/go-srs/core"
	"os"
)

func run(svr *app.Server) int {
	d := new(daemon.Context)
	var c *os.Process
	if app.GsConfig.Daemon {
		core.GsTrace.Println("run in daemon mode, log file", app.GsConfig.Log.File)
		if child, err := d.Reborn(); err != nil {
			core.GsError.Println("daemon failed. err is", err)
			return -1
		} else {
			c = child
		}
	}
	defer d.Release()

	if c != nil {
		os.Exit(0)
	}

	return serve(svr)
}

func srsMain(svr *app.Server) {
	core.GsTrace.Println("SRS start serve, pid is", os.Getpid(), "and ppid is", os.Getppid())
}
