package kernel

import (
	"math/rand"
	"sync/atomic"
	"time"
)

// CircuitBreaker 是状态机级断路器，单节点连续超时/5xx 自动熔断，
// 降级重试时配合 JitterBackoff 消除雪崩惊群。
type CircuitBreaker struct {
	state         atomic.Int32 // 0=closed 1=open 2=half_open
	failures      atomic.Int32
	halfOpenCount atomic.Int32
	openSinceNano atomic.Int64

	threshold   int
	openTimeout time.Duration
	halfOpenMax int
}

const (
	breakerClosed   int32 = 0
	breakerOpen     int32 = 1
	breakerHalfOpen int32 = 2
)

func NewCircuitBreaker(threshold, openTimeoutSec, halfOpenMax int) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if openTimeoutSec <= 0 {
		openTimeoutSec = 30
	}
	if halfOpenMax <= 0 {
		halfOpenMax = 3
	}
	return &CircuitBreaker{
		threshold:   threshold,
		openTimeout: time.Duration(openTimeoutSec) * time.Second,
		halfOpenMax: halfOpenMax,
	}
}

// Allow 判断是否放行本次请求，并按需完成 half_open 探测。
func (cb *CircuitBreaker) Allow() bool {
	switch cb.state.Load() {
	case breakerClosed:
		return true
	case breakerOpen:
		if time.Since(time.Unix(0, cb.openSinceNano.Load())) >= cb.openTimeout {
			if cb.state.CompareAndSwap(breakerOpen, breakerHalfOpen) {
				cb.halfOpenCount.Store(0)
			}
			return true
		}
		return false
	case breakerHalfOpen:
		if int(cb.halfOpenCount.Add(1)) <= cb.halfOpenMax {
			return true
		}
		cb.halfOpenCount.Add(-1)
		return false
	}
	return true
}

func (cb *CircuitBreaker) State() string {
	switch cb.state.Load() {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.failures.Store(0)
	if cb.state.Load() == breakerHalfOpen {
		cb.state.Store(breakerClosed)
		cb.halfOpenCount.Store(0)
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	if cb.state.Load() == breakerHalfOpen {
		cb.state.Store(breakerOpen)
		cb.openSinceNano.Store(time.Now().UnixNano())
		return
	}
	if cb.failures.Add(1) >= int32(cb.threshold) {
		if cb.state.CompareAndSwap(breakerClosed, breakerOpen) {
			cb.openSinceNano.Store(time.Now().UnixNano())
		}
	}
}

// JitterBackoff 返回带随机抖动的指数退避时长，消除雪崩惊群效应。
func JitterBackoff(base time.Duration, attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	exp := base
	for i := 0; i < attempt && exp < time.Minute; i++ {
		exp *= 2
	}
	if exp > time.Minute {
		exp = time.Minute
	}
	jitter := time.Duration(rand.Int63n(int64(exp)/2 + 1))
	return exp + jitter
}
