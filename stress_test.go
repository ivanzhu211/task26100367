package pulselink

import (
	"math/rand"
	"runtime"
	"testing"
	"time"
)

// 可复现压力测试：100 万条不同心跳，含大量超过 300 秒的空档，
// 分别以逆序与随机顺序到达，随后 1 万次不同终点查询。
// 运行：go test -run TestStress -v
func TestStress(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式下跳过压力测试")
	}
	const n = 1_000_000
	const queries = 10_000

	// 固定种子生成时间轴：平均间隔 400s，大量空档超过 300s。
	rng := rand.New(rand.NewSource(42))
	beats := make([]int64, n)
	ts := int64(0)
	for i := range beats {
		beats[i] = ts
		ts += 1 + rng.Int63n(800_000) // 间隔 1ms..800s，约 62% 超过 300s
	}
	span := ts

	for _, mode := range []string{"reverse", "random"} {
		order := make([]int64, n)
		if mode == "reverse" {
			for i := range order {
				order[i] = beats[n-1-i]
			}
		} else {
			copy(order, beats)
			rand.New(rand.NewSource(7)).Shuffle(n, func(i, j int) {
				order[i], order[j] = order[j], order[i]
			})
		}

		runtime.GC()
		var memBefore runtime.MemStats
		runtime.ReadMemStats(&memBefore)

		m := NewMonitor()
		m.Register("dev", 0)

		start := time.Now()
		for _, b := range order {
			m.RecordHeartbeat("dev", b)
		}
		insertElapsed := time.Since(start)

		start = time.Now()
		qrng := rand.New(rand.NewSource(99))
		for i := 0; i < queries; i++ {
			endAt := 1 + qrng.Int63n(span)
			s := m.Summarize("dev", endAt)
			windows := []struct {
				w   WindowSummary
				len int64
			}{
				{s.Day, min(endAt, windowDay)},
				{s.Week, min(endAt, windowWeek)},
				{s.Month, min(endAt, windowMonth)},
			}
			for _, c := range windows {
				if c.w.Online+c.w.Degraded+c.w.Offline != c.len {
					t.Fatalf("%s: endAt=%d 窗口长度恒等式不成立: %d+%d+%d != %d",
						mode, endAt, c.w.Online, c.w.Degraded, c.w.Offline, c.len)
				}
			}
		}
		queryElapsed := time.Since(start)

		runtime.GC()
		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)
		runtime.KeepAlive(m) // 防止监测器在统计前被回收

		t.Logf("[%s] 插入 %d 条: %v (%.0f ns/条), 查询 %d 次(3窗口): %v (%.0f ns/次), 保留堆内存: %.1f MiB",
			mode, n, insertElapsed, float64(insertElapsed.Nanoseconds())/n,
			queries, queryElapsed, float64(queryElapsed.Nanoseconds())/queries,
			float64(int64(memAfter.HeapAlloc)-int64(memBefore.HeapAlloc))/1048576)
	}
}
