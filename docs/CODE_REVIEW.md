# Code Review: pmux

## Executive Summary

This codebase is well-architected with a clean separation of concerns. However, several bugs, security issues, and refactoring opportunities were identified. This review categorizes findings by severity.

---

## Critical Bugs

### 1. TCP Connection Leak / Potential Panic (`internal/proxy/server.go:260-274`)

```go
func (s *Server) handleTCP(src net.Conn, targetPort int) {
    defer src.Close()

    dst, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort), 5*time.Second)
    if err != nil {
        log.Printf("tcp proxy: dial failed: %v", err)
        return  // BUG: dst is nil here!
    }
    defer dst.Close()  // PANIC: nil pointer dereference
    // ...
}
```

**Fix**: Check if `dst` is non-nil before deferring close, or use a local function.

---

### 2. TCP Listeners Not Cleaned Up on Route Changes (`internal/proxy/server.go:308-327`)

The `watchRoutes` function reloads routes but never removes TCP listeners for routes that were deleted. This causes:
- Stale listeners consuming ports
- Routes re-added without listeners starting

**Fix**: In `loadRoutes`, diff the old vs new TCP routes and call `stopTCPListener` for removed ones.

---

### 3. Race Condition in Signal Handling (`internal/process/spawn.go:84-88`)

```go
case sig := <-sigChan:
    fmt.Printf("\nReceived %v, cleaning up...\n", sig)
    _ = cmd.Process.Signal(sig)
    <-done  // Wait for process but not signal handler!
    return nil
```

If another signal arrives during cleanup, it may be lost. Also, the deferred `cleanup()` runs after return, but `signal.Stop()` was already called at line 21.

---

### 4. DNS Server Error Handling (`internal/dns/server.go:34-44`)

```go
errCh := make(chan error, 2)
go func() { errCh <- s.udp.ListenAndServe() }()
go func() { errCh <- s.tcp.ListenAndServe() }()

select {
case err := <-errCh:
    return nil, err  // Only catches first error!
default:
}
```

If both UDP and TCP fail, only one error is reported. Additionally, if both succeed initially but one later fails, the error is never caught.

---

## Security Vulnerabilities

### 5. Command Injection via `--name` Flag (`cmd/run.go:101`)

```go
opts.Name = args[i]
// ... passed to domain.Generate() -> sanitize()
// sanitize() only keeps [a-zA-Z0-9-]
```

Actually, this is **safe** — the `sanitize()` function at `internal/domain/generator.go:72-77` properly escapes shell metacharacters. However, documentation should clarify this is validated.

---

### 6. Insufficient TLS Key Permissions (`internal/proxy/certs.go:124-131`)

Keys are written with default umask (likely 0644), making them readable by other users:

```go
func writePEM(path, pemType string, data []byte) error {
    f, err := os.Create(path)  // Uses umask, not 0600!
    // ...
}
```

**Fix**: Use `os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)`

---

### 7. PID File Permissions (`internal/proxy/server.go:364-366`)

```go
return os.WriteFile(PidFile(configDir), []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
```

Any local user can overwrite this and potentially cause issues.

---

### 8. Host Header Injection / DNS Rebinding (`internal/proxy/server.go:171-191`)

The proxy trusts the `Host` header without validation:

```go
host := r.Host
// Strip port but NO validation that host matches expected domain pattern
```

A malicious client could send arbitrary Host headers. While the routing logic matches against known routes, this could leak information about internal network topology.

---

### 9. CA Trust Check is Flawed (`internal/platform/platform.go:109-111`)

```go
err := exec.Command("security", "verify-cert", "-c", caCertPath, "-p", "ssl").Run()
```

This only verifies the cert is valid for SSL, not that it's trusted as a root CA. Should check for "trustRoot" usage.

---

## Refactoring Opportunities

### 10. Duplicate Code: `loadUnsafe()` vs `LoadRoutes()`

`pkg/config/config.go:34-55` and `122-138` are nearly identical. `LoadRoutes` acquires a lock and calls `loadUnsafe`. Consider having `LoadRoutes` call `loadUnsafe` directly.

---

### 11. Duplicate Git Command Execution

`internal/domain/generator.go:39-54` runs git commands separately:
```go
if out, err := exec.Command("git", "rev-parse", "--git-common-dir").Output(); err == nil {
    // worktree detection
}
if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
    // branch name
}
```

Could batch into a single command.

---

### 12. Unused Error in TLS Setup (`internal/proxy/server.go:104-106`)

```go
tlsConfig, err := s.buildTLSConfig()
if err != nil {
    log.Printf("warning: TLS setup failed: %v (HTTPS disabled)", err)
    // BUG: s.tlsEnabled not set to false!
}
```

Should set `s.tlsEnabled = false` on failure.

---

### 13. Missing Port Validation

No validation that user-provided ports are in valid range (1-65535). Negative or >65535 values could cause unexpected behavior.

---

### 14. Magic Numbers Should Be Constants

Extract to named constants:
- `500 * time.Millisecond` (route polling)
- `5 * time.Second` (TCP dial timeout)
- `100 * time.Millisecond` (startup wait loops)
- `20` (proxy start retry count)

---

### 15. Context: `ProxyRun` Missing Error Return

`cmd/proxy.go:87`:
```go
return srv.Run()  // Run() returns error but is ignored in some call paths
```

Ensure all callers handle the error.

---

## Code Quality Issues

### 16. No Test Coverage

As noted in `ARCHITECTURE.md:188`, zero test coverage exists. Priority areas for tests:
- `port.Find()` - easy to unit test
- `domain.Generate()` - deterministic
- `config.Store` - file I/O mocking

---

### 17. Inconsistent Error Handling Style

- `internal/proxy/server.go:78`: Logs warning and continues
- `internal/proxy/server.go:84`: Logs warning and continues  
- `internal/process/spawn.go:38`: Prints to stderr

Standardize on either returning errors or logging consistently.

---

### 18. Graceful Shutdown Has No Timeout

`internal/proxy/server.go:146-149`:
```go
if dnsServer != nil {
    dnsServer.Stop()
}
return s.shutdown()  // No context.WithTimeout
```

In-flight DNS queries or HTTP requests could hang indefinitely.

---

## Minor Issues

### 19. `RootDomain` Error Discarded

`internal/domain/generator.go:50-54`: If `os.Getwd()` fails after git worktree check fails, error is discarded.

### 20. TCP Handler Only Waits One Direction

`internal/proxy/server.go:270-273`:
```go
done := make(chan struct{}, 2)
go func() { io.Copy(dst, src); done <- struct{}{} }()
go func() { io.Copy(src, dst); done <- struct{}{} }()
<-done  // Only waits for ONE to finish!
```

Both directions should complete before closing. Should use `<-done` twice or a `sync.WaitGroup`.

---

## Summary Table

| Issue | Severity | Category |
|-------|----------|----------|
| TCP nil pointer panic | Critical | Bug |
| TCP listener cleanup | Critical | Bug |
| TLS key permissions | High | Security |
| PID file permissions | Medium | Security |
| DNS error handling | Medium | Bug |
| Host header validation | Medium | Security |
| CA trust check | Medium | Security |
| Route watching | Medium | Bug |
| Signal race condition | Medium | Bug |
| Duplicate code | Low | Refactor |
| Magic numbers | Low | Refactor |
| No tests | Low | Quality |

---

## Recommendations

1. **Immediate**: Fix the TCP nil pointer panic (issue #1)
2. **Immediate**: Add TCP listener cleanup on route changes (issue #2)
3. **High**: Fix TLS key permissions (issue #6)
4. **High**: Add proper port validation
5. **Medium**: Add unit tests for core packages
6. **Medium**: Extract magic numbers to constants
7. **Low**: Refactor duplicate code patterns
