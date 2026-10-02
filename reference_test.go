package pulselink

import (
	"math/rand"
	"sort"
	"testing"
)

// slowReference 是独立的慢速参考实现：保存全部心跳，
// 每次查询时排序去重后逐段累加，允许 O(n log n)。
type slowReference struct {
	activeAt int64
	beats    map[int64]struct{}
}

func newSlowReference(activeAt int64) *slowReference {
	return &slowReference{activeAt: activeAt, beats: make(map[int64]struct{})}
}

func (r *slowReference) record(ts int64) {
	r.beats[ts] = struct{}{}
}

func (r *slowReference) summarize(endAt int64) Summary {
	sorted := make([]int64, 0, len(r.beats))
	for ts := range r.beats {
		sorted = append(sorted, ts)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return Summary{
		Day:   slowWindow(r.activeAt, sorted, endAt, windowDay),
		Week:  slowWindow(r.activeAt, sorted, endAt, windowWeek),
		Month: slowWindow(r.activeAt, sorted, endAt, windowMonth),
	}
}

func slowWindow(activeAt int64, beats []int64, endAt, window int64) WindowSummary {
	lo := max(activeAt, endAt-window)
	var online, degraded, offline int64
	// 状态发生变化的临界点：窗口端点、心跳时刻、心跳后 90s/300s。
	points := []int64{lo, endAt}
	for _, b := range beats {
		for _, p := range []int64{b, b + onlineLimit, b + offlineLimit} {
			if p > lo && p < endAt {
				points = append(points, p)
			}
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })
	for i := 0; i+1 < len(points); i++ {
		a, b := points[i], points[i+1]
		if a >= b {
			continue
		}
		// 区间 [a,b) 内无临界点，状态由 a 时刻决定。
		last := int64(-1)
		for _, hb := range beats {
			if hb <= a && hb > last {
				last = hb
			}
		}
		d := b - a
		switch {
		case last < 0:
			offline += d
		case a-last < onlineLimit:
			online += d
		case a-last < offlineLimit:
			degraded += d
		default:
			offline += d
		}
	}
	length := endAt - lo
	return WindowSummary{
		Online:   online,
		Degraded: degraded,
		Offline:  offline,
		Uptime:   float64(online) / float64(length),
	}
}

// 随机乱序 + 重复输入，每次操作后逐次与参考实现核对。
func TestRandomizedAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261003))
	for trial := 0; trial < 30; trial++ {
		activeAt := rng.Int63n(1000)
		m := NewMonitor()
		m.Register("dev", activeAt)
		ref := newSlowReference(activeAt)

		now := activeAt
		ops := 60 + rng.Intn(120)
		for op := 0; op < ops; op++ {
			now += 1 + rng.Int63n(400000) // 制造大量超过 300s 的空档
			if rng.Intn(3) > 0 {
				// 乱序：在过去与未来之间随机取采样时刻，含重复。
				ts := activeAt + rng.Int63n(now-activeAt+1)
				m.RecordHeartbeat("dev", ts)
				ref.record(ts)
				if rng.Intn(4) == 0 {
					m.RecordHeartbeat("dev", ts) // 重复提交
				}
			} else {
				endAt := activeAt + 1 + rng.Int63n(now-activeAt+1)
				got := m.Summarize("dev", endAt)
				want := ref.summarize(endAt)
				if got != want {
					t.Fatalf("trial %d op %d endAt=%d:\ngot  %+v\nwant %+v",
						trial, op, endAt, got, want)
				}
			}
		}
		// 全部到达后，覆盖所有已见终点再核对一次。
		for i := 0; i < 20; i++ {
			endAt := activeAt + 1 + rng.Int63n(now-activeAt+1)
			if got, want := m.Summarize("dev", endAt), ref.summarize(endAt); got != want {
				t.Fatalf("trial %d final %d endAt=%d:\ngot  %+v\nwant %+v",
					trial, i, endAt, got, want)
			}
		}
	}
}
