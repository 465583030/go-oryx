/*
The MIT License (MIT)

Copyright (c) 2013-2015 SRS(simple-rtmp-server)

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/

package core

import (
    "runtime"
    "time"
)

type Server struct {
    logger *simpleLogger
}

func NewServer() *Server {
    svr := &Server{
        logger: &simpleLogger{},
    }

    GsConfig.Subscribe(svr)

    return svr
}

func (s *Server) Close() {
    GsConfig.Unsubscribe(s)
}

func (s *Server) ParseConfig(conf string) (err error) {
    LoggerInfo.Println("start to parse config file", conf)
    if err = GsConfig.Loads(conf); err != nil {
        return
    }

    return
}

func (s *Server) PrepareLogger() (err error) {
    if err = s.applyLogger(GsConfig); err != nil {
        return
    }

    return
}

func (s *Server) Initialize() (err error) {
    // reload goroutine
    go ReloadWorker()

    return
}

func (s *Server) Run() (err error) {
    s.applyMultipleProcesses(GsConfig.Workers)

    for {
        runtime.GC()
        LoggerInfo.Println("go runtime gc every", GsConfig.Go.GcInterval, "seconds")
        time.Sleep(time.Second * time.Duration(GsConfig.Go.GcInterval))
    }

    return
}

// interface ReloadHandler
func (s *Server) OnReloadGlobal(scope int, cc, pc *Config) (err error) {
    if scope == ReloadWorkers {
        s.applyMultipleProcesses(cc.Workers)
    } else if scope == ReloadLog {
        s.applyLogger(cc)
    }

    return
}

func (s *Server) applyMultipleProcesses(workers int) {
    pv := runtime.GOMAXPROCS(workers)
    LoggerTrace.Println("apply workers", workers, "and previous is", pv)
}

func (s *Server) applyLogger(c *Config) (err error) {
    if err = s.logger.Close(c); err != nil {
        return
    }
    LoggerInfo.Println("close logger ok")

    if err = s.logger.Open(c); err != nil {
        return
    }
    LoggerInfo.Println("open logger ok")

    return
}
