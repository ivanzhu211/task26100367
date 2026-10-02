package pulselink

import (
	"math/rand"
	"testing"
)

func checkWindow(t *testing.T, name string, w WindowSummary, length, on, deg, off int64) {
	t.Helper()
	if w.Online != on || w.Degraded != deg || w.Offline != off {
		t.Errorf("%s: got (%d,%d,%d), want (%d,%d,%d)", name, w.Online, w.Degraded, w.Offline, on, deg, off)
	}
	if w.Online+w.Degraded+w.Offline != length {
		t.Errorf("%s: durations sum %d != window length %d", name, w.Online+w.Degraded+w.Offline, length)
	}
	wantUp := float64(on) / float64(length)
	if w.Uptime != wantUp {
		t.Errorf("%s: uptime %v != %v", name, w.Uptime, wantUp)
	}
}

// 题目给定用例：先收到 0 和 400000，查询终点 450000；补传 200000 后结果改变。
func TestSpecExample(t *testing.T) {
	m := NewMonitor()
	m.Register("d", 0)
	m.RecordHeartbeat("d", 0)
	m.RecordHeartbeat("d", 400000)
	s := m.Summarize("d", 450000)
	checkWindow(t, "24h before backfill", s.Day, 450000, 140000, 210000, 100000)

	m.RecordHeartbeat("d", 200000)
	s = m.Summarize("d", 450000)
	checkWindow(t, "24h after backfill", s.Day, 450000, 230000, 220000, 0)
}

// 重复提交与不同到达顺序必须得到完全一致的结果。
func TestDuplicateAndOrderInvariance(t *testing.T) {
	beats := []int64{0, 200000, 400000, 85000, 1_000_000, 300000}
	orders := [][]int64{
		{0, 1, 2, 3, 4, 5},
		{5, 4, 3, 2, 1, 0},
		{2, 0, 5, 1, 4, 3},
	}
	var want Summary
	for i, ord := range orders {
		m := NewMonitor()
		m.Register("d", 0)
		for _, j := range ord {
			m.RecordHeartbeat("d", beats[j])
			m.RecordHeartbeat("d", beats[j]) // 重复提交
		}
		// 穿插查询不应影响后续结果
		_ = m.Summarize("d", 500000)
		m.RecordHeartbeat("d", beats[ord[0]]) // 再次重复
		got := m.Summarize("d", 1_200_000)
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("order %v: got %+v, want %+v", ord, got, want)
		}
	}
}

// 恰好达到 90s 进入 Degraded，恰好达到 300s 进入 Offline。
func TestExactThresholds(t *testing.T) {
	m := NewMonitor()
	m.Register("d", 0)
	m.RecordHeartbeat("d", 0)

	s := m.Summarize("d", 90000)
	checkWindow(t, "end=90000", s.Day, 90000, 90000, 0, 0)

	s = m.Summarize("d", 90001)
	checkWindow(t, "end=90001", s.Day, 90001, 90000, 1, 0)

	s = m.Summarize("d", 300000)
	checkWindow(t, "end=300000", s.Day, 300000, 90000, 210000, 0)

	s = m.Summarize("d", 300001)
	checkWindow(t, "end=300001", s.Day, 300001, 90000, 210000, 1)
}

// 监测时间不足完整窗口时，窗口左端取监测起点。
func TestShortMonitoringWindow(t *testing.T) {
	m := NewMonitor()
	m.Register("d", 1_000_000_000)
	m.RecordHeartbeat("d", 1_000_000_000)
	s := m.Summarize("d", 1_000_000_000+1000)
	checkWindow(t, "24h short", s.Day, 1000, 1000, 0, 0)
	checkWindow(t, "7d short", s.Week, 1000, 1000, 0, 0)
	checkWindow(t, "30d short", s.Month, 1000, 1000, 0, 0)
}

// 窗口左端之前的心跳仍决定窗口内状态；右端及之后的心跳不贡献。
func TestHeartbeatsAroundWindowEdges(t *testing.T) {
	m := NewMonitor()
	m.Register("d", 0)
	m.RecordHeartbeat("d", 0) // 在窗口左端之前

	end := int64(Window24h) + 5000 // 窗口 [5000, end)
	s := m.Summarize("d", end)
	checkWindow(t, "left-edge heartbeat", s.Day, Window24h, 85000, 210000, Window24h-295000)

	// 恰在右端的心跳不贡献（右开区间）。
	m2 := NewMonitor()
	m2.Register("d", 0)
	m2.RecordHeartbeat("d", 0)
	m2.RecordHeartbeat("d", 400000)
	s2 := m2.Summarize("d", 400000)
	checkWindow(t, "heartbeat at right edge", s2.Day, 400000, 90000, 210000, 100000)

	// 右端之后的心跳不贡献。
	m2.RecordHeartbeat("d", 450000)
	s2 = m2.Summarize("d", 400000)
	checkWindow(t, "heartbeat after right edge", s2.Day, 400000, 90000, 210000, 100000)

	// 恰在左端的心跳属于窗口。
	m3 := NewMonitor()
	m3.Register("d", 0)
	m3.RecordHeartbeat("d", 0)
	m3.RecordHeartbeat("d", 5000)
	s3 := m3.Summarize("d", int64(Window24h)+5000)
	checkWindow(t, "heartbeat at left edge", s3.Day, Window24h, 90000, 210000, Window24h-300000)
}

// 首条心跳之前为 Offline。
func TestOfflineBeforeFirstHeartbeat(t *testing.T) {
	m := NewMonitor()
	m.Register("d", 0)
	m.RecordHeartbeat("d", 100000)
	s := m.Summarize("d", 100000)
	checkWindow(t, "before first heartbeat", s.Day, 100000, 0, 0, 100000)
}

// 补传一条不改变最新心跳的旧心跳也要改写历史。
func TestLateBackfillRewritesHistory(t *testing.T) {
	build := func() *Monitor {
		m := NewMonitor()
		m.Register("d", 0)
		m.RecordHeartbeat("d", 0)
		m.RecordHeartbeat("d", 1_000_000)
		return m
	}
	m := build()
	before := m.Summarize("d", 1_000_000)
	m.RecordHeartbeat("d", 500000) // 旧心跳，不改变最新心跳
	after := m.Summarize("d", 1_000_000)
	if before == after {
		t.Fatal("late backfill should rewrite history")
	}
	checkWindow(t, "after backfill", after.Day, 1_000_000, 180000, 420000, 400000)
}

// ---- 慢速参考实现：O(n) 扫描，用于随机对照 ----

type refDevice struct {
	activeAt int64
	beats    map[int64]struct{}
}

type refMonitor struct {
	devices map[string]*refDevice
}

func newRefMonitor() *refMonitor { return &refMonitor{devices: map[string]*refDevice{}} }

func (r *refMonitor) register(id string, activeAt int64) {
	if _, ok := r.devices[id]; !ok {
		r.devices[id] = &refDevice{activeAt: activeAt, beats: map[int64]struct{}{}}
	}
}

func (r *refMonitor) record(id string, ts int64) {
	r.devices[id].beats[ts] = struct{}{}
}

// stateAt 返回 t 时刻距最后心跳的距离；无心跳返回 -1。
func (d *refDevice) lastBeatBefore(t int64) (int64, bool) {
	best := int64(-1 << 62)
	found := false
	for b := range d.beats {
		if b <= t && b > best {
			best = b
			found = true
		}
	}
	return best, found
}

func (d *refDevice) window(endAt, win int64) WindowSummary {
	left := d.activeAt
	if endAt-win > left {
		left = endAt - win
	}
	var on, deg, off int64
	// 逐段扫描：在 [left, endAt) 上按心跳切分，逐毫秒状态由 lastBeat 决定。
	// 为保持 O(n) 而非逐毫秒，收集区间内关键点。
	points := []int64{left, endAt}
	for b := range d.beats {
		if b > left && b < endAt {
			points = append(points, b)
		}
	}
	// 简单插入排序，n 小
	for i := 1; i < len(points); i++ {
		for j := i; j > 0 && points[j] < points[j-1]; j-- {
			points[j], points[j-1] = points[j-1], points[j]
		}
	}
	for i := 0; i+1 < len(points); i++ {
		a, b := points[i], points[i+1]
		// 段 [a,b) 内状态由 a 时刻（段内不变？不——状态随 t 变化，但只依赖 t-lastBeat）
		// lastBeat 在段内恒定，状态边界在 lastBeat+90000 / +300000 处。
		lb, ok := d.lastBeatBefore(a)
		seg := func(from, to int64) {
			if to <= from {
				return
			}
			if !ok {
				off += to - from
				return
			}
			dist := func(x int64) int64 { return x - lb }
			// [from,to) 按距离切分
			x := from
			for _, boundary := range []int64{lb + onlineThreshold, lb + degradedThreshold, to} {
				y := min(boundary, to)
				if y > x {
					switch boundary {
					case lb + onlineThreshold:
						on += y - x
					case lb + degradedThreshold:
						deg += y - x
					default:
						off += y - x
					}
					x = y
				}
			}
			_ = dist
		}
		seg(a, b)
	}
	length := endAt - left
	return WindowSummary{Online: on, Degraded: deg, Offline: off, Uptime: float64(on) / float64(length)}
}

func (r *refMonitor) summarize(id string, endAt int64) Summary {
	d := r.devices[id]
	return Summary{
		Day:   d.window(endAt, Window24h),
		Week:  d.window(endAt, Window7d),
		Month: d.window(endAt, Window30d),
	}
}

// 随机乱序 + 重复 + 穿插查询，逐次与参考实现核对。
func TestRandomizedAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	m := NewMonitor()
	ref := newRefMonitor()
	const dev = "d"
	m.Register(dev, 0)
	ref.register(dev, 0)

	for i := 0; i < 400; i++ {
		switch rng.Intn(3) {
		case 0, 1: // 心跳，小范围时间制造重复与乱序
			ts := rng.Int63n(2_000_000)
			m.RecordHeartbeat(dev, ts)
			ref.record(dev, ts)
		case 2: // 查询并核对
			end := 1 + rng.Int63n(2_500_000)
			got := m.Summarize(dev, end)
			want := ref.summarize(dev, end)
			if got != want {
				t.Fatalf("iter %d end=%d:\n got  %+v\n want %+v", i, end, got, want)
			}
		}
	}
}

// 大时间尺度版本：心跳稀疏且跨度超过 30 天，三个窗口都会触发左端裁剪。
func TestRandomizedLargeScale(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	m := NewMonitor()
	ref := newRefMonitor()
	const dev = "d"
	base := int64(100 * Window30d)
	m.Register(dev, base)
	ref.register(dev, base)

	for i := 0; i < 300; i++ {
		switch rng.Intn(3) {
		case 0, 1:
			// 跨度 40 天，间隔从秒级到多天不等，含大量超 300s 空档
			ts := base + rng.Int63n(int64(40*Window24h))
			m.RecordHeartbeat(dev, ts)
			ref.record(dev, ts)
		case 2:
			end := base + 1 + rng.Int63n(int64(45*Window24h))
			got := m.Summarize(dev, end)
			want := ref.summarize(dev, end)
			if got != want {
				t.Fatalf("iter %d end=%d:\n got  %+v\n want %+v", i, end, got, want)
			}
		}
	}
}
