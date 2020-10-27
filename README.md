Simple hierarchical Go logger

[![GitHub tag (latest SemVer)](https://img.shields.io/github/tag/rsms/go-log.svg)][godoc]
[![PkgGoDev](https://pkg.go.dev/badge/github.com/rsms/go-log)][godoc]
[![Go Report Card](https://goreportcard.com/badge/github.com/rsms/go-log)](https://goreportcard.com/report/github.com/rsms/go-log)

- Serializes all logs on a "background" goroutine
- If the output writer is a TTY, use terminal colors (disable by unsetting `FColor`)
- Hierarchical; `SubLogger` creates a logger with shared output, level and prefix

## Example

```go
func main() {
  log.RootLogger.EnableFeatures(log.FMicroseconds)

  log.Info("Hello")

  log.RootLogger.Level = log.LevelDebug
  log.Debug("Wild %#v", Things{})

  fooLogger := log.SubLogger("[foo]")
  fooLogger.Warn("Danger, Will Robinson")
}
```

```
12:54:34.794802 [info] Hello
12:54:34.794826 [debug] Wild Things{Field:0} (log_test.go:18)
12:54:34.794887 [warn] [foo] Danger, Will Robinson
```

See [documentation][godoc] or `log_test.go` for more details.


[godoc]: https://pkg.go.dev/github.com/rsms/go-log
