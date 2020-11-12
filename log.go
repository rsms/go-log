package log

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Level defines the log level
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelDisable // log nothing

	// used by Time
	levelTime

	// internal control messages between the logger and its writeLoop
	ctlSync // synchronize
)

// behavior
type Features int

const (
	FDate         Features = 1 << iota // include date (format YYYY-MM-DD)
	FTime                              // include time (format HH:MM:SS)
	FMilliseconds                      // include milliseconds in time (format Time.sss)
	FMicroseconds                      // include microseconds in time (format Time.ssssss)
	FUTC                               // date & time in UTC rather than local time zone
	FDebugOrigin                       // include source file at end of debug messages
	FColor                             // enable ANSI terminal colors
	FColorAuto                         // enable FColor if w is TTY & env TERM supports colors

	fPrefixStart   = 0xff
	fPrefixBitOffs = 8

	FPrefixDebug = 1 << (fPrefixBitOffs + LevelDebug) // enable prefix for LevelDebug ("[debug]")
	FPrefixInfo  = 1 << (fPrefixBitOffs + LevelInfo)  // enable prefix for LevelInfo ("[info]")
	FPrefixWarn  = 1 << (fPrefixBitOffs + LevelWarn)  // enable prefix for LevelWarn ("[warn]")
	FPrefixError = 1 << (fPrefixBitOffs + LevelError) // enable prefix for LevelError ("[error]")

	fSyncStart   = 0xffff
	fSyncBitOffs = 16

	FSyncDebug = 1 << (fSyncBitOffs + LevelDebug) // write debug messages in a blocking fashion
	FSyncInfo  = 1 << (fSyncBitOffs + LevelInfo)  // write info messages in a blocking fashion
	FSyncWarn  = 1 << (fSyncBitOffs + LevelWarn)  // write warning messages in a blocking fashion
	FSyncError = 1 << (fSyncBitOffs + LevelError) // write error messages in a blocking fashion

	FSync    = FSyncDebug | FSyncInfo | FSyncWarn | FSyncError
	FDefault = FTime | FDebugOrigin | FColorAuto |
		FPrefixDebug | FPrefixInfo | FPrefixWarn | FPrefixError
)

type Logger struct {
	Level
	Features
	Prefix string

	parent *Logger // non-nil for sub-loggers
	w      io.Writer
	qch    chan *logRecord // may be shared by multiple loggers
	syncch chan error
}

var RootLogger = NewLogger(os.Stdout, "", LevelInfo, FDefault)

func Error(format string, v ...interface{})       { RootLogger.Error(format, v...) }
func Warn(format string, v ...interface{})        { RootLogger.Warn(format, v...) }
func Info(format string, v ...interface{})        { RootLogger.Info(format, v...) }
func Debug(format string, v ...interface{})       { RootLogger.LogDebug(1, format, v...) }
func Time(format string, v ...interface{}) func() { return RootLogger.Time(format, v...) }
func Printf(format string, v ...interface{})      { RootLogger.Info(format, v...) }
func SubLogger(extraPrefix string) *Logger        { return RootLogger.SubLogger(extraPrefix) }
func Sync()                                       { RootLogger.Sync() }

// NewLogger makes a new logger that is writing to w
func NewLogger(w io.Writer, prefix string, level Level, feats Features) *Logger {
	if feats&FColorAuto != 0 {
		feats = featuresWithAutoColor(w, feats)
	}
	// feats = feats &^ FColor // XXX
	l := &Logger{
		Level:    level,
		Features: feats,
		Prefix:   prefix,
		w:        w,
		qch:      make(chan *logRecord, 100),
		syncch:   make(chan error),
	}
	go l.writeLoop()
	return l
}

func (l *Logger) SubLogger(addPrefix string) *Logger {
	l2 := *l // shallow copy
	l2.Prefix = l2.Prefix + addPrefix
	l2.parent = l
	return &l2
}

func (l *Logger) Close() {
	if l.parent == nil {
		l.Sync()
		close(l.qch)
	} else {
		// sub logger
		l.Level = LevelDisable
		l.qch = nil
	}
}

// Sync returns when all messages have been written.
// If the process exits after a Sync call all messages up to that point are guaranteed to be
// written, assuming the OS kernel doesn't terminate (i.e. from power failure.)
func (l *Logger) Sync() error {
	m := logRecordFree.Get().(*logRecord)
	m.level = ctlSync
	l.qch <- m
	return <-l.syncch
}

func (l *Logger) EnableFeatures(enableFeats Features) {
	if enableFeats&FColorAuto != 0 && l.Features&FColor == 0 {
		// maybe turn on FColor
		enableFeats = featuresWithAutoColor(l.w, enableFeats)
	}
	l.Features |= enableFeats
}

func (l *Logger) DisableFeatures(disableFeats Features) {
	if disableFeats&FColorAuto != 0 && l.Features&FColorAuto == 0 {
		// turn off FColor if FColorAuto is enabled
		disableFeats |= FColor
	}
	l.Features = l.Features &^ disableFeats
}

func (l *Logger) Writer() io.Writer {
	return l.w
}

func (l *Logger) SetWriter(w io.Writer) {
	l.w = w
}

func (l *Logger) Error(format string, v ...interface{}) {
	if l.Level <= LevelError {
		l.log(LevelError, format, v...)
	}
}

func (l *Logger) Warn(format string, v ...interface{}) {
	if l.Level <= LevelWarn {
		l.log(LevelWarn, format, v...)
	}
}

func (l *Logger) Info(format string, v ...interface{}) {
	if l.Level <= LevelInfo {
		l.log(LevelInfo, format, v...)
	}
}

func (l *Logger) Debug(format string, v ...interface{}) {
	l.LogDebug(1, format, v...)
}

func (l *Logger) LogDebug(calldepth int, format string, v ...interface{}) {
	if l.Level <= LevelDebug {
		if l.Features&FDebugOrigin != 0 {
			var file string
			var line int
			var ok bool
			_, file, line, ok = runtime.Caller(calldepth + 1)
			if !ok {
				file = "???"
				line = 0
			} else {
				// simplify /path/to/dir/file.go -> dir/file.go
				file = simplifySrcFilename(file)
			}
			if l.Features&FColor != 0 {
				format = format + " \x1b[90m(%s:%d)\x1b[39m"
			} else {
				format = format + " (%s:%d)"
			}
			v = append(v, file, line)
		}
		l.log(LevelDebug, format, v...)
	}
}

var initTime = time.Now()

// Time starts a time measurement, logged when the returned function is invoked. Uses LevelInfo.
// Call the returned function to measure time taken since the call to l.Time and log a message.
//   "thing with 123: 6.597116ms"
//
// Example: Measure time spent in a function:
//
//   func foo(thing int) {
//     defer log.Time("foo with thing %d", thing)()
//     ...
//   }
// Output:
//   "[time] foo with thing 123: 6.597116ms"
//
func (l *Logger) Time(format string, v ...interface{}) func() {
	if l.Level > LevelInfo {
		return func() {}
	}
	// Note: Windows uses a low-res timer for time.Now (Oct 2020)
	// See https://go-review.googlesource.com/c/go/+/227499/
	start := time.Since(initTime)
	msg := fmt.Sprintf(format, v...) // must evaluate asap in case v contains pointers
	return func() {
		format := "%s: %s"
		if len(msg) == 0 {
			format = "%s%s"
		}
		l.log(levelTime, format, msg, time.Since(initTime)-start)
	}
}

func (l *Logger) Log(level Level, format string, v ...interface{}) {
	if l.Level <= level {
		l.log(level, format, v...)
	}
}

// alternate method spelling for compatibility with e.g. badger
func (l *Logger) Errorf(format string, v ...interface{})   { l.Error(format, v...) }
func (l *Logger) Warningf(format string, v ...interface{}) { l.Warn(format, v...) }
func (l *Logger) Infof(format string, v ...interface{})    { l.Info(format, v...) }
func (l *Logger) Debugf(format string, v ...interface{})   { l.Debug(format, v...) }

// GoLogger returns a go log.Logger that mirrors this logger.
// Useful for APIs that specifically requires a go Logger.
//
// forLevel should be the level of logging that the Go logger will be used for.
// For example, if this is to be used for debugging, call GoLogger(LevelDebug).
// In case forLevel is less than l.Level a null logger is returned. This way the level of
// the receiver has an effect on the Go logger.
//
// Example:
//   logger.Level = log.LevelWarn
//   goLoggerInfo := logger.GoLogger(log.LevelInfo)
//   goLoggerWarn := logger.GoLogger(log.LevelWarn)
//   goLoggerInfo.Printf("Hello")  // (nothing is printed)
//   goLoggerWarn.Printf("oh no")  // "oh no" is printed
//
func (l *Logger) GoLogger(forLevel Level) *log.Logger {
	var flag int
	if l.Features&FDate != 0 {
		flag |= log.Ldate
	}
	if l.Features&FTime != 0 {
		flag |= log.Ltime
	}
	if l.Features&(FMilliseconds|FMicroseconds) != 0 {
		flag |= log.Lmicroseconds
	}
	if l.Features&FUTC != 0 {
		flag |= log.LUTC
	}
	if l.Features&FDebugOrigin != 0 {
		flag |= log.Lshortfile
	}
	w := l.w
	if forLevel < l.Level {
		w = ioutil.Discard
	}
	return log.New(w, l.Prefix, flag)
}

// ——————————————————————————————————————————————————————————————————————————————————————————————
// package internal

type logRecord struct {
	logger *Logger
	level  Level
	time   time.Time
	msg    []byte
}

// free list (note: go's fmt package uses this so it is definitely "fast enough")
var logRecordFree = sync.Pool{
	New: func() interface{} { return new(logRecord) },
}

// free saves used pp structs in ppFree; avoids an allocation per invocation.
func (m *logRecord) free() {
	// From go's fmt package:
	//   Proper usage of a sync.Pool requires each entry to have approximately
	//   the same memory cost. To obtain this property when the stored type
	//   contains a variably-sized buffer, we add a hard limit on the maximum buffer
	//   to place back in the pool.
	//   See https://golang.org/issue/23199
	if cap(m.msg) > 4<<10 {
		return
	}
	m.logger = nil
	m.msg = m.msg[:0]
	logRecordFree.Put(m)
}

func (m *logRecord) write(buf *[]byte) error {
	m.logger.formatHeader(buf, m.time, m.level)
	*buf = append(*buf, m.msg...)
	if len(m.msg) == 0 || m.msg[len(m.msg)-1] != '\n' {
		*buf = append(*buf, '\n')
	}
	_, err := m.logger.w.Write(*buf)
	m.free()
	return err
}

func (l *Logger) log(level Level, format string, v ...interface{}) {
	m := logRecordFree.Get().(*logRecord)
	m.logger = l
	m.level = level
	m.time = time.Now()
	// must format now rather than in m.write since v may contain pointers
	if len(v) == 0 {
		m.msg = append(m.msg, format...)
	} else {
		s := fmt.Sprintf(format, v...)
		m.msg = append(m.msg, s...)
	}
	if Features(1<<(fSyncBitOffs+level))&l.Features != 0 {
		var bufa [256]byte
		buf := bufa[:]
		m.write(&buf)
	} else {
		l.qch <- m
	}
}

func featuresWithAutoColor(w io.Writer, feats Features) Features {
	// enable FColor if w is a TTY and env $TERM seems to support color
	if f, ok := w.(*os.File); ok {
		if st, _ := f.Stat(); (st.Mode() & os.ModeCharDevice) != 0 {
			TERM := os.Getenv("TERM")
			if strings.Contains(TERM, "xterm") ||
				strings.Contains(TERM, "vt100") ||
				strings.Contains(TERM, "color") {
				feats |= FColor
			}
		}
	}
	return feats
}

// writeLoop
func (l *Logger) writeLoop() {
	var buf []byte
	var err error
	for {
		m, more := <-l.qch
		if m.level == ctlSync {
			l.syncch <- err // return last write error
		} else {
			buf = buf[:0] // reset buffer
			err = m.write(&buf)
		}
		if !more {
			break
		}
	}
}

// formatHeader writes log header to buf in following order:
//   - date and/or time (if corresponding flags are provided)
//   - levelPrefix[level]
//   - prefix
// Adapted from go/src/log/log.go
func (l *Logger) formatHeader(buf *[]byte, t time.Time, level Level) {
	if l.Features&(FDate|FTime|FMilliseconds|FMicroseconds) != 0 {
		if l.Features&FColor != 0 {
			*buf = append(*buf, colorFgGrey...)
		}
		if l.Features&FUTC != 0 {
			t = t.UTC()
		}
		if l.Features&FDate != 0 {
			year, month, day := t.Date()
			itoa(buf, year, 4)
			*buf = append(*buf, '-')
			itoa(buf, int(month), 2)
			*buf = append(*buf, '-')
			itoa(buf, day, 2)
			*buf = append(*buf, ' ')
		}
		if l.Features&(FTime|FMilliseconds|FMicroseconds) != 0 {
			hour, min, sec := t.Clock()
			itoa(buf, hour, 2)
			*buf = append(*buf, ':')
			itoa(buf, min, 2)
			*buf = append(*buf, ':')
			itoa(buf, sec, 2)
			if l.Features&(FMilliseconds|FMicroseconds) != 0 {
				*buf = append(*buf, '.')
				ns := t.Nanosecond()
				if l.Features&FMicroseconds != 0 {
					itoa(buf, ns/1e3, 6)
				} else {
					itoa(buf, ns/1e6, 3)
				}
			}
			*buf = append(*buf, ' ')
		}
		if l.Features&FColor != 0 {
			*buf = append(*buf, colorFgReset...)
		}
	}
	if Features(1<<(fPrefixBitOffs+level))&l.Features != 0 {
		if l.Features&FColor != 0 {
			*buf = append(*buf, levelPrefixColor[level]...)
		} else {
			*buf = append(*buf, levelPrefixPlain[level]...)
		}
	}
	if len(l.Prefix) > 0 {
		*buf = append(*buf, l.Prefix...)
		*buf = append(*buf, ' ')
	}
}

// Cheap integer to fixed-width decimal ASCII. Give a negative width to avoid zero-padding.
// From go/src/log/log.go
func itoa(buf *[]byte, i int, wid int) {
	// Assemble decimal in reverse order.
	var b [20]byte
	bp := len(b) - 1
	for i >= 10 || wid > 1 {
		wid--
		q := i / 10
		b[bp] = byte('0' + i - q*10)
		bp--
		i = q
	}
	// i < 10
	b[bp] = byte('0' + i)
	*buf = append(*buf, b[bp:]...)
}

func simplifySrcFilename(file string) string {
	if wd, err := os.Getwd(); err == nil {
		if len(file) > len(wd) && file[len(wd)] == os.PathSeparator && strings.HasPrefix(file, wd) {
			// rooted in working directory
			return file[len(wd)+1:]
		}
	}
	// return a short version that contains the first parent dir + basename.
	// i.e. /foo/bar/baz.go -> bar/baz.go, /baz.go -> baz.go
	short := file
	for i := len(file) - 1; i > 0; i-- {
		if file[i] == '/' {
			short = file[i+1:]
			i--
			// find second '/' (if not found use short as is)
			for ; i > 0; i-- {
				if file[i] == '/' {
					short = file[i+1:]
					break
				}
			}
			break
		}
	}
	return short
}

// ——————————————————————————————————————————————————————————————————————————————————————————————
// data

const (
	colorFgGrey  = "\x1b[90m"
	colorFgReset = "\x1b[39m"
)

var (
	levelPrefixPlain = [6]string{
		"[debug] ",
		"[info] ",
		"[warn] ",
		"[error] ",
		"", // disabled; ignore
		"[time] ",
	}

	levelPrefixColor = [6]string{
		"\x1b[90m[\x1b[34;1m" + "debug" + "\x1b[22;90m]\x1b[39m ",
		"\x1b[90m[\x1b[39;1m" + "info" + "\x1b[90m]\x1b[22;39m ",
		"\x1b[90m[\x1b[33;1m" + "warn" + "\x1b[22;90m]\x1b[39m ",
		"\x1b[90m[\x1b[31;1m" + "error" + "\x1b[22;90m]\x1b[39m ",
		"", // disabled; ignore
		"\x1b[90m[\x1b[36;1m" + "time" + "\x1b[22;90m]\x1b[39m ",
	}
	// 1  bold on
	// 22 bold off
	// foreground color:
	//   30 black
	//   31 red
	//   32 green
	//   33 yellow
	//   34 blue
	//   35 magenta
	//   36 cyan
	//   37 white
	//   38 start 256-color (next is "5;n" or "2;r;g;b")
	//   39 standard (reset foreground color)
	//   90 gray
	// background color:
	//   40 black
	//   41 red
	//   42 green
	//   43 yellow
	//   44 blue
	//   45 magenta
	//   46 cyan
	//   47 white
	//   48 start 256-color (next is "5;n" or "2;r;g;b")
	//   49 standard (reset foreground color)
)

/*
-----------------------------------------------------
Template for main program
-----------------------------------------------------

func loge(format string, v ...interface{}) {
  log.Error(format, v...)
}
func logw(format string, v ...interface{}) {
  log.Warn(format, v...)
}
func logi(format string, v ...interface{}) {
  log.Info(format, v...)
}
func logd(format string, v ...interface{}) {
  log.Debug(format, v...)
}

-----------------------------------------------------
Template for type
-----------------------------------------------------

func (s *TYPE) loge(format string, v ...interface{}) {
  s.Logger.Error(format, v...)
}
func (s *TYPE) logw(format string, v ...interface{}) {
  s.Logger.Warn(format, v...)
}
func (s *TYPE) logi(format string, v ...interface{}) {
  s.Logger.Info(format, v...)
}
func (s *TYPE) logd(format string, v ...interface{}) {
  s.Logger.Debug(format, v...)
}

*/
