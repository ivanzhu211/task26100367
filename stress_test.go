package pulselink

import (
	"math/rand"
	"runtime"
	"testing"
	"time"
)

// 压力测试：100 万条不同心跳随机乱序到达，含大量超过 300s 的空档，
// 随后 1 万次不同终点查询。记录耗时与保留内存。
// 运行：go test -run TestStress -v
func TestStressMillionHeartbeats(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test skipped in -short mode")
	}
	const n = 1_000_000
	const queries = 10_000

	// 生成时间轴：约 90% 为 1s 密集间隔，10% 为 400s~700s 大空档，
	// 保证数据包含大量 Offline 区间而非连续 Online。
	beats := make([]int64, n)
	cur := int64(1_000_000)
	rng := rand.New(rand.NewSource(2026))
	for i := 0; i < n; i++ {
		if i%10 == 0 {
			cur += 400_000 + rng.Int63n(300_000) // 大空档：400s~700s
		} else {
			cur += 1_000 + rng.Int63n(2_000) // 密集：1s~3s
		}
		beats[i] = cur
	}
	span := cur

	// 随机乱序到达（固定种子，可复现）。
	order := rng.Perm(n)

	m := NewMonitor()
	m.Register("dev", 0)

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	start := time.Now()
	for _, idx := range order {
		m.RecordHeartbeat("dev", beats[idx])
	}
	insertElapsed := time.Since(start)

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	// 1 万次不同终点查询，同时校验三状态时长之和等于窗口长度。
	start = time.Now()
	var checksum int64
	for q := 0; q < queries; q++ {
		end := 1 + rng.Int63n(span)
		s := m.Summarize("dev", end)
		for _, w := range []WindowSummary{s.Day, s.Week, s.Month} {
			length := w.Online + w.Degraded + w.Offline
			wantLen := min(end, int64(Window24h))
			switch w {
			case s.Week:
				wantLen = min(end, int64(Window7d))
			case s.Month:
				wantLen = min(end, int64(Window30d))
			}
			if length != wantLen {
				t.Fatalf("q=%d end=%d: invalid window length %d", q, end, length)
			}
			if w.Uptime < 0 || w.Uptime > 1 {
				t.Fatalf("q=%d end=%d: invalid uptime %v", q, end, w.Uptime)
			}
			checksum += w.Online
		}
	}
	queryElapsed := time.Since(start)

	t.Logf("heartbeats: %d distinct, span %d ms (%.1f days)", n, span, float64(span)/float64(Window24h))
	t.Logf("insert: %v total, %v/op amortized", insertElapsed, insertElapsed/n)
	t.Logf("queries: %d x 3 windows in %v, %v/query", queries, queryElapsed, queryElapsed/queries)
	t.Logf("retained heap: %.1f MB (%.1f bytes/heartbeat)",
		float64(memAfter.HeapAlloc-memBefore.HeapAlloc)/(1<<20),
		float64(memAfter.HeapAlloc-memBefore.HeapAlloc)/n)
	t.Logf("checksum: %d", checksum)
}
