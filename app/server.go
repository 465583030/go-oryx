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

package app

import (
	"fmt"
	"github.com/ossrs/go-oryx/core"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
	"github.com/ossrs/go-oryx/agent"
)

// the state of server, state graph:
//      Init => Normal(Ready => Running)
//      Init/Normal => Closed
type ServerState int

const (
	StateInit ServerState = 1 << iota
	StateReady
	StateRunning
	StateClosed
)

type Server struct {
	// signal handler.
	sigs chan os.Signal
	// whether closed.
	closed  ServerState
	closing chan bool
	// for system internal to notify quit.
	quit chan bool
	wg   sync.WaitGroup
	// core components.
	htbt   *Heartbeat
	logger *simpleLogger
	rtmp core.Agent
	// the locker for state, for instance, the closed.
	lock sync.Mutex
}

func NewServer() *Server {
	svr := &Server{
		sigs:    make(chan os.Signal, 1),
		closed:  StateInit,
		closing: make(chan bool, 1),
		quit:    make(chan bool, 1),
		htbt:    NewHeartbeat(),
		logger:  &simpleLogger{},
	}

	core.Conf.Subscribe(svr)

	return svr
}

// notify server to stop and wait for cleanup.
func (s *Server) Close() {
	// wait for stopped.
	s.lock.Lock()
	defer s.lock.Unlock()

	// closed?
	if s.closed == StateClosed {
		core.Info.Println("server already closed.")
		return
	}

	// notify to close.
	if s.closed == StateRunning {
		core.Info.Println("notify server to stop.")
		select {
		case s.quit <- true:
		default:
		}
	}

	// wait for closed.
	if s.closed == StateRunning {
		<-s.closing
	}

	// do cleanup when stopped.
	core.Conf.Unsubscribe(s)

	// ok, closed.
	s.closed = StateClosed
	core.Info.Println("server closed")
}

func (s *Server) ParseConfig(conf string) (err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.closed != StateInit {
		panic("server invalid state.")
	}
	s.closed = StateReady

	core.Trace.Println("start to parse config file", conf)
	if err = core.Conf.Loads(conf); err != nil {
		return
	}

	return
}

func (s *Server) PrepareLogger() (err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.closed != StateReady {
		panic("server invalid state.")
	}

	if err = s.applyLogger(core.Conf); err != nil {
		return
	}

	return
}

func (s *Server) Initialize() (err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.closed != StateReady {
		panic("server invalid state.")
	}

	// install signals.
	// TODO: FIXME: when process the current signal, others may drop.
	signal.Notify(s.sigs)

	// use worker container to fork.
	var wc core.WorkerContainer = s

	// reload goroutine
	wc.GFork("reload", core.Conf.ReloadCycle)
	// heartbeat goroutine
	wc.GFork("htbt(discovery)", s.htbt.discoveryCycle)
	wc.GFork("htbt(main)", s.htbt.beatCycle)
	// rtmp agent.
	if s.rtmp,err = agent.NewRtmpPublish(wc); err != nil {
		core.Error.Println("create rtmp agent failed. err is", err)
		return
	}

	c := core.Conf
	l := fmt.Sprintf("%v(%v/%v)", c.Log.Tank, c.Log.Level, c.Log.File)
	if !c.LogToFile() {
		l = fmt.Sprintf("%v(%v)", c.Log.Tank, c.Log.Level)
	}
	core.Trace.Println(fmt.Sprintf("init server ok, conf=%v, log=%v, workers=%v/%v, gc=%v, daemon=%v",
		c.Conf(), l, c.Workers, runtime.NumCPU(), c.Go.GcInterval, c.Daemon))

	return
}

func (s *Server) Run() (err error) {
	func() {
		s.lock.Lock()
		defer s.lock.Unlock()

		if s.closed != StateReady {
			panic("server invalid state.")
		}
		s.closed = StateRunning
	}()

	// when terminated, notify the chan.
	defer func() {
		select {
		case s.closing <- true:
		default:
		}
	}()

	core.Info.Println("server running")

	// run server, apply settings.
	s.applyMultipleProcesses(core.Conf.Workers)

	var wc core.WorkerContainer = s
	for {
		select {
		case signal := <-s.sigs:
			core.Trace.Println("got signal", signal)
			switch signal {
			case os.Interrupt, syscall.SIGTERM:
				// SIGINT, SIGTERM
				wc.Quit()
			}
		case <-wc.QC():
			wc.Quit()

			// wait for all goroutines quit.
			s.wg.Wait()
			core.Warn.Println("server quit")
			return
		case <-time.After(time.Second * time.Duration(core.Conf.Go.GcInterval)):
			runtime.GC()
			core.Info.Println("go runtime gc every", core.Conf.Go.GcInterval, "seconds")
		}
	}

	return
}

// interface WorkContainer
func (s *Server) QC() <-chan bool {
	return s.quit
}

func (s *Server) Quit() {
	select {
	case s.quit <- true:
	default:
	}
}

func (s *Server) GFork(name string, f func(core.WorkerContainer)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		defer func() {
			if r := recover(); r != nil {
				core.Error.Println(name, "worker panic:", r)
				s.Quit()
			}
		}()

		f(s)
		core.Trace.Println(name, "worker terminated.")
	}()
}

// interface ReloadHandler
func (s *Server) OnReloadGlobal(scope int, cc, pc *core.Config) (err error) {
	if scope == core.ReloadWorkers {
		s.applyMultipleProcesses(cc.Workers)
	} else if scope == core.ReloadLog {
		s.applyLogger(cc)
	}

	return
}

func (s *Server) applyMultipleProcesses(workers int) {
	if workers < 0 {
		panic("should not be negative workers")
	}

	if workers == 0 {
		workers = runtime.NumCPU()
	}
	pv := runtime.GOMAXPROCS(workers)

	core.Trace.Println("apply workers", workers, "and previous is", pv)
}

func (s *Server) applyLogger(c *core.Config) (err error) {
	if err = s.logger.close(c); err != nil {
		return
	}
	core.Info.Println("close logger ok")

	if err = s.logger.open(c); err != nil {
		return
	}
	core.Info.Println("open logger ok")

	return
}
