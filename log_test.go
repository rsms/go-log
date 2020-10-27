package log

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/rsms/go-testutil"
)

type Things struct {
	Field int
}

func testConfigureRootLogger() *bytes.Buffer {
	w := &bytes.Buffer{}
	if testing.Verbose() {
		RootLogger.SetWriter(io.MultiWriter(w, os.Stdout))
	} else {
		RootLogger.SetWriter(w)
	}
	return w
}

func TestLog(t *testing.T) {
	assert := testutil.NewAssert(t)
	w := testConfigureRootLogger()

	RootLogger.DisableFeatures(FColor)
	RootLogger.EnableFeatures(FMicroseconds)
	RootLogger.Level = LevelDebug

	fooLogger := SubLogger("[foo]")

	timer := Time("time something")
	Debug("Wild %#v", Things{})
	Info("Hello")
	Printf("Printf is the same as Info")
	fooLogger.Warn("Danger, Will Robinson")
	fooLogger.Error("I'm afraid I can't do that")
	timer()

	Sync()

	// Expected output
	expectedLines := []string{
		"[debug] Wild log.Things{Field:0} (log_test.go",
		"[info] Hello",
		"[info] Printf is the same as Info",
		"[warn] [foo] Danger, Will Robinson",
		"[error] [foo] I'm afraid I can't do that",
		"[time] time something:",
	}
	lines := bytes.Split(w.Bytes(), []byte("\n"))
	assert.Eq("num lines", len(expectedLines), len(lines)-1) // -1 because last line is empty
	for i, expectedPrefix := range expectedLines {
		lineWithoutTimestamp := lines[i][16:]
		expectLen, lineLen := len(expectedPrefix), len(lineWithoutTimestamp)
		if assert.Ok("len(lines[%d]) >= %d; got %d", lineLen >= expectLen, i, expectLen, lineLen) {
			assert.Eq("line %d", lineWithoutTimestamp[:len(expectedPrefix)], []byte(expectedPrefix), i)
		}
	}
	assert.Eq("last line is empty", lines[len(expectedLines)], []byte{})
}

func TestLogSerialization(t *testing.T) {
	// Note: This test should be run with a timeout (e.g. `go test -timeout 1s`)

	assert := testutil.NewAssert(t)
	w := &bytes.Buffer{}
	// RootLogger.SetWriter(io.MultiWriter(w, os.Stdout)) // uncomment to print all output
	RootLogger.SetWriter(w)

	// number of goroutines to run
	N := 3

	// lines that each goroutine will write, in order. Each line's value must be unique.
	writeMessages := []string{
		"aaaaaa",
		"bbbbbb",
		"cccccc",
		"dddddd",
		"eeeeee",
		"ffffff",
		"gggggg",
		"hhhhhh",
		"iiiiii",
		"jjjjjj",
	}
	syncch := make(chan bool)

	// deterministic rand so we get the same behavior on every run
	rnd := rand.New(rand.NewSource(123))

	// start N goroutines all writing the same messages
	for i := 0; i < N; i++ {
		go func(goroutineId int) {
			for _, line := range writeMessages {
				Info("%s %d", line, goroutineId)
				// introduce some jitter
				time.Sleep(time.Duration(rnd.Intn(5000)) * time.Microsecond)
			}
			syncch <- true
		}(i)
	}

	// wait for all to finish
	for i := 0; i < N; i++ {
		<-syncch
	}

	Sync()

	// verify that each line was written exactly once per goroutine
	lines := bytes.Split(w.Bytes(), []byte("\n"))

	if !assert.Eq("num lines", N*len(writeMessages), len(lines)-1) { // -1 since last line is empty
		return
	}

	seenmap := make(map[string][]string, N) // gorotineId => seenMessages
	for i := 0; i < N*len(writeMessages); i++ {
		line := lines[i]
		parts := bytes.Split(line, []byte(" "))
		if !assert.Eq("line has 4 parts; got %q", len(parts), 4, line) {
			break
		}
		message, gorotineId := string(parts[2]), string(parts[3])
		// t.Logf("%q, %q", gorotineId, message)
		seenMessages := seenmap[gorotineId]
		if seenMessages == nil {
			seenmap[gorotineId] = []string{message}
		} else {
			for y := 0; y < len(seenMessages); y++ {
				if assert.Ok("duplicate message %q", seenMessages[y] != message, message) {
					return
				}
			}
			seenmap[gorotineId] = append(seenMessages, message)
		}
	}

	// verify messages were written in order
	for _, seenMessages := range seenmap {
		if assert.Eq("message count", len(seenMessages), len(writeMessages)) {
			for i, seenMessage := range seenMessages {
				assert.Eq("messsage order", seenMessage, writeMessages[i])
			}
		}
	}
}
