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
    "os"
    "io/ioutil"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "os/signal"
    "syscall"
)

const (
    ReloadWorkers = iota
    ReloadLog
)

// the reload handler,
// the client which care about the reload event,
// must implements this interface and then register itself
// to the config.
type ReloadHandler interface {
    // when reload the global scopes,
    // for example, the workers, listen and log.
    // @param scope defined in const ReloadXXX.
    // @param cc the current loaded config, GsConfig.
    // @param pc the previous old config.
    OnReloadGlobal(scope int, cc, pc *Config) error
}

// the config for this application,
// which can load from file in json style,
// and convert to json string.
// @remark user can user the GsConfig object.
type Config struct {
    // the global section.
    Workers int `json:"workers"` // the number of cpus to use

    // the rtmp global section.
    Listen int `json:"listen"` // the system service RTMP listen port

    // the go section.
    Go struct {
        GcInterval int `json:"gc_interval"` // the gc interval in seconds.
    }

    // the log config.
    Log struct {
        Tank string `json:"tank"` // the log tank, file or console
        Level string `json:"level"` // the log level, info/trace/warn/error
        File string `json:"file"` // for log tank file, the log file path.
    } `json:"log"`

    conf string `json:"-"` // the config file path.
    reloadHandlers []ReloadHandler `json:"-"`
}

// the current global config.
var GsConfig = NewConfig()

func NewConfig() *Config {
    return &Config{
        reloadHandlers: []ReloadHandler{},
    }
}

// loads and validate config from config file.
func (c *Config) Loads(conf string) error {
    c.conf = conf

    if f,err := os.Open(conf); err != nil {
        return err
    } else if s,err := ioutil.ReadAll(f); err != nil {
        return err
    } else if err := json.Unmarshal([]byte(s), c); err != nil {
        return err
    } else {
        return c.Validate()
    }
}

// validate the config whether ok.
func (c *Config) Validate() error {
    if c.Log.Level == "info" {
        LoggerWarn.Println("info level hurts performance")
    }

    if c.Workers <= 0 || c.Workers > 64 {
        return errors.New(fmt.Sprintf("workers must in (0, 64], actual is %v", c.Workers))
    }
    if c.Listen <= 0 || c.Listen > 65535 {
        return errors.New(fmt.Sprintf("listen must in (0, 65535], actual is %v", c.Listen))
    }

    if c.Go.GcInterval <= 0 || c.Go.GcInterval > 24 * 3600 {
        return errors.New(fmt.Sprintf("go gc_interval must in (0, 24*3600], actual is %v", c.Go.GcInterval))
    }

    if c.Log.Level != "info" && c.Log.Level != "trace" && c.Log.Level != "warn" && c.Log.Level != "error" {
        return errors.New(fmt.Sprintf("log.leve must be info/trace/warn/error, actual is %v", c.Log.Level))
    }
    if c.Log.Tank != "console" && c.Log.Tank != "file" {
        return errors.New(fmt.Sprintf("log.tank must be console/file, actual is %v", c.Log.Tank))
    }
    if c.Log.Tank == "file" && len(c.Log.File) == 0 {
        return errors.New("log.file must not be empty for file tank")
    }

    return nil
}

// whether log tank is file
func (c *Config) LogToFile() bool {
    return c.Log.Tank == "file"
}

// get the log tank writer for specified level.
// the param dw is the default writer.
func (c *Config) LogTank(level string, dw io.Writer) io.Writer {
    if c.Log.Level == "info" {
        return dw
    }
    if c.Log.Level == "trace" {
        if level == "info" {
            return ioutil.Discard
        }
        return dw
    }
    if c.Log.Level == "warn" {
        if level == "info" || level == "trace" {
            return ioutil.Discard
        }
        return dw
    }
    if c.Log.Level == "error" {
        if level != "error" {
            return ioutil.Discard
        }
        return dw
    }

    return ioutil.Discard
}

// subscribe the reload event,
// when got reload event, notify all handlers.
func (c *Config) Subscribe(h ReloadHandler) {
    // ignore exists.
    for _,v := range c.reloadHandlers {
        if v == h {
            return
        }
    }

    c.reloadHandlers = append(c.reloadHandlers, h)
}

func (c* Config) Unsubscribe(h ReloadHandler) {
    for i,v := range c.reloadHandlers {
        if v == h {
            c.reloadHandlers = append(c.reloadHandlers[:i], c.reloadHandlers[i+1:]...)
            return
        }
    }
}

// the goroutine worker for reload.
func ReloadWorker() {
    signals := make(chan os.Signal, 1)
    // 1: SIGHUP
    signal.Notify(signals, syscall.Signal(1))

    // process all reload signals.
    func(){
        defer func(){
            if r := recover(); r != nil {
                LoggerError.Println("reload panic:", r)
            }
        }()

        LoggerTrace.Println("wait for reload signals: kill -1", os.Getpid())
        for signal := range signals {
            LoggerTrace.Println("reload by", signal)

            pc := GsConfig
            cc := NewConfig()
            cc.reloadHandlers = pc.reloadHandlers[:]
            if err := cc.Loads(GsConfig.conf); err != nil {
                LoggerError.Println("reload config failed. err is", err)
                continue
            }
            LoggerInfo.Println("reload parse fresh config ok")

            if err := doReload(cc, pc); err != nil {
                LoggerError.Println("apply reload failed. err is", err)
                continue
            }
            LoggerInfo.Println("reload completed work")

            GsConfig = cc
            LoggerTrace.Println("reload config ok")
        }
    }()
}

func doReload(cc, pc *Config) (err error) {
    if cc.Workers != pc.Workers {
        for _, h := range cc.reloadHandlers {
            if err = h.OnReloadGlobal(ReloadWorkers, cc, pc); err != nil {
                return
            }
        }
        LoggerTrace.Println("reload apply workers ok")
    } else {
        LoggerInfo.Println("reload ignore workers")
    }

    if cc.Log.File != pc.Log.File || cc.Log.Level != pc.Log.Level || cc.Log.Tank != pc.Log.Tank {
        for _, h := range cc.reloadHandlers {
            if err = h.OnReloadGlobal(ReloadLog, cc, pc); err != nil {
                return
            }
        }
        LoggerTrace.Println("reload apply log ok")
    } else {
        LoggerInfo.Println("reload ignore log")
    }

    return
}

