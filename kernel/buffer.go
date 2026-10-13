package kernel

import (
	"errors"
	"io"
	"sync"
)

// ErrBodyTooLarge 表示请求体超过 max_request_body_bytes 上限（backlog #7）。
var ErrBodyTooLarge = errors.New("request body too large")

// PoolBufSize 是池化切片的固定尺寸，锁定数据流转的内存上界。
const PoolBufSize = 32 * 1024

// BufferPool 是基于 sync.Pool 的全局字节缓冲区对象池，
// 同时实现 httputil.BufferPool 接口，供 ReverseProxy 零拷贝中转复用。
type BufferPool struct {
	pool sync.Pool
}

func NewBufferPool() *BufferPool {
	return &BufferPool{pool: sync.Pool{New: func() any {
		return make([]byte, PoolBufSize)
	}}}
}

// Get 从池中借出固定尺寸切片（len == cap == PoolBufSize）。
func (p *BufferPool) Get() []byte {
	return p.pool.Get().([]byte)
}

// Put 归还切片；尺寸不符的缓冲直接丢弃，避免污染池。
func (p *BufferPool) Put(b []byte) {
	if cap(b) == PoolBufSize {
		p.pool.Put(b[:PoolBufSize])
	}
}

// 全局单例池：贯穿请求中转、Body 改写与流式解析的所有字节缓冲。
var globalPool = NewBufferPool()

func GetBuffer() []byte  { return globalPool.Get() }
func PutBuffer(b []byte) { globalPool.Put(b) }

// ReadBody 读取请求体全部内容。区别于 io.ReadAll，它使用池化固定尺寸
// 切片作为中转暂存，杜绝反复动态扩容带来的零散大内存分配。
// maxBytes > 0 时限额读取：累计超过即返回 ErrBodyTooLarge，绝不多读（内存防线 层 1）。
func ReadBody(r io.Reader, maxBytes int64) ([]byte, error) {
	buf := GetBuffer()
	defer PutBuffer(buf)
	var out []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
			if maxBytes > 0 && int64(len(out)) > maxBytes {
				return nil, ErrBodyTooLarge
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
	}
}
