package kernel

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dezd-ai/ai-gateway-core/config"
)

// EdgeStorage 是边缘环形时序落盘器（TSDB 前端）与异步上报队列。
// 断网时高频流式追加至本地 uba.json，达阈值 FIFO 轮转覆盖，死守不因 Disk Full 崩溃。
type EdgeStorage struct {
	mu        sync.Mutex
	path      string
	maxBytes  int64
	strategy  string
	reportURL string
	file      *os.File

	queue chan []byte
	stop  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
}

func NewEdgeStorage(cfg config.EdgeStorage, logPath string) *EdgeStorage {
	es := &EdgeStorage{
		path:      logPath,
		maxBytes:  cfg.MaxDiskMB * 1024 * 1024,
		strategy:  cfg.Strategy,
		reportURL: cfg.ReportURL,
		queue:     make(chan []byte, 4096),
		stop:      make(chan struct{}),
	}
	if es.maxBytes <= 0 {
		es.maxBytes = 512 * 1024 * 1024
	}
	if cfg.Enabled {
		es.open()
		es.wg.Add(1)
		go es.reportLoop()
	}
	return es
}

func (es *EdgeStorage) open() {
	dir := filepath.Dir(es.path)
	if dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(es.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		// 兜底写入当前目录，避免权限问题导致审计链路中断
		f, err = os.OpenFile("uba.json", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
	}
	es.file = f
}

// Append 追加一条结构化 JSON 审计记录（本地落盘 + 非阻塞入队上报）。
func (es *EdgeStorage) Append(record []byte) {
	if es == nil {
		return
	}
	line := make([]byte, 0, len(record)+1)
	line = append(line, record...)
	line = append(line, '\n')

	es.mu.Lock()
	if es.file != nil {
		_, _ = es.file.Write(line)
		es.checkRotateLocked()
	}
	es.mu.Unlock()

	select {
	case es.queue <- record:
	default:
		// 上报队列满：本地文件已保真，仅丢弃远端上报，绝不阻塞网关主链路
	}
}

func (es *EdgeStorage) checkRotateLocked() {
	if es.file == nil {
		return
	}
	st, err := es.file.Stat()
	if err != nil {
		return
	}
	if st.Size() < es.maxBytes {
		return
	}
	// FIFO 轮转覆盖：当前文件归档为 .1（覆盖上一代最旧段），重新开新文件
	_ = es.file.Close()
	_ = os.Remove(es.path + ".1")
	_ = os.Rename(es.path, es.path+".1")
	es.open()
}

func (es *EdgeStorage) reportLoop() {
	defer es.wg.Done()
	client := &http.Client{Timeout: 10 * time.Second}
	for {
		select {
		case <-es.stop:
			return
		case rec := <-es.queue:
			es.reportWithBackoff(client, rec)
		}
	}
}

func (es *EdgeStorage) reportWithBackoff(client *http.Client, rec []byte) {
	if es.reportURL == "" {
		return
	}
	for attempt := 0; attempt < 6; attempt++ {
		resp, err := client.Post(es.reportURL, "application/json", bytes.NewReader(rec))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
		time.Sleep(JitterBackoff(time.Second, attempt))
	}
}

func (es *EdgeStorage) Close() {
	if es == nil {
		return
	}
	es.once.Do(func() {
		close(es.stop)
		es.wg.Wait()
		es.mu.Lock()
		if es.file != nil {
			_ = es.file.Close()
		}
		es.mu.Unlock()
	})
}
