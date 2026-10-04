package sdk

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Logger is the minimal logging surface the grpc-free core needs. hclog.Logger
// satisfies it, so hosts can inject their own logger; the default is a tiny
// stdlib logger, which keeps hclog (and its transitive deps) out of Native
// plugin builds (Go plugin requires the host and plugin to share identical
// dependency versions).
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

const (
	levelDebug = iota
	levelInfo
	levelWarn
	levelError
)

type stdLogger struct {
	mu    sync.Mutex
	name  string
	level int
}

func newStdLogger(name string) *stdLogger {
	return &stdLogger{name: name, level: levelInfo}
}

func (l *stdLogger) SetLevelName(name string) {
	lvl := levelInfo
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "DEBUG", "TRACE":
		lvl = levelDebug
	case "INFO", "":
		lvl = levelInfo
	case "WARNING", "WARN":
		lvl = levelWarn
	case "ERROR", "CRITICAL":
		lvl = levelError
	}
	l.mu.Lock()
	l.level = lvl
	l.mu.Unlock()
}

func (l *stdLogger) emit(lv int, tag, msg string, args ...any) {
	l.mu.Lock()
	enabled := lv >= l.level
	l.mu.Unlock()
	if !enabled {
		return
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "[%s] %s: %s %v\n", tag, l.name, msg, args)
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", tag, l.name, msg)
}

func (l *stdLogger) Debug(msg string, args ...any) { l.emit(levelDebug, "DEBUG", msg, args...) }
func (l *stdLogger) Info(msg string, args ...any)  { l.emit(levelInfo, "INFO", msg, args...) }
func (l *stdLogger) Warn(msg string, args ...any)  { l.emit(levelWarn, "WARN", msg, args...) }
func (l *stdLogger) Error(msg string, args ...any) { l.emit(levelError, "ERROR", msg, args...) }
