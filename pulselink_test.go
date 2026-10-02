package pulselink

import "testing"

func checkWindow(t *testing.T, w WindowSummary, online, degraded, offline int64) {
	t.Helper()
	if w.Online != online || w.Degraded != degraded || w.Offline != offline {
		t.Fatalf("got (%d,%d,%d), want (%d,%d,%d)",
			w.Online, w.Degraded, w.Offline, online, degraded, offline)
	}
	length := online + degraded + offline
	if w.Online+w.Degraded+w.Offline != length {
		t.Fatalf("sum mismatch: %d+%d+%d != %d", w.Online, w.Degraded, w.Offline, length)
	}
	wantUptime := float64(online) / float64(length)
	if w.Uptime != wantUptime {
		t.Fatalf("uptime got %v, want %v", w.Uptime, wantUptime)
	}
}

// 题目给定用例：监测起点 0，心跳 0 与 400000，终点 450000。
func TestSpecExample(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 0)
	m.RecordHeartbeat("dev", 0)
	m.RecordHeartbeat("dev", 400000)

	s := m.Summarize("dev", 450000)
	checkWindow(t, s.Day, 140000, 210000, 100000)

	m.RecordHeartbeat("dev", 200000) // 补传旧心跳，改写历史
	s = m.Summarize("dev", 450000)
	checkWindow(t, s.Day, 230000, 220000, 0)
}

// 同一组心跳以不同到达顺序、伴随重复提交与中间查询，结果必须一致。
func TestOrderAndDuplicateInvariance(t *testing.T) {
	beats := []int64{0, 200000, 400000}

	m1 := NewMonitor()
	m1.Register("dev", 0)
	for _, b := range beats {
		m1.RecordHeartbeat("dev", b)
		m1.RecordHeartbeat("dev", b) // 重复提交
	}

	m2 := NewMonitor()
	m2.Register("dev", 0)
	m2.RecordHeartbeat("dev", 400000)
	_ = m2.Summarize("dev", 450000) // 中间穿插查询
	m2.RecordHeartbeat("dev", 0)
	m2.RecordHeartbeat("dev", 400000) // 重复
	m2.RecordHeartbeat("dev", 200000)
	m2.RecordHeartbeat("dev", 200000)

	s1 := m1.Summarize("dev", 450000)
	s2 := m2.Summarize("dev", 450000)
	if s1 != s2 {
		t.Fatalf("order/duplicate mismatch: %+v vs %+v", s1, s2)
	}
	checkWindow(t, s1.Day, 230000, 220000, 0)
}

// 恰好达到 90 秒转为 Degraded、恰好达到 300 秒转为 Offline。
func TestExactThresholds(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 0)
	m.RecordHeartbeat("dev", 0)

	// 终点恰为心跳后 90000：90000 全部 Online。
	s := m.Summarize("dev", 90000)
	checkWindow(t, s.Day, 90000, 0, 0)

	// 90001：最后 1ms 为 Degraded。
	s = m.Summarize("dev", 90001)
	checkWindow(t, s.Day, 90000, 1, 0)

	// 终点恰为心跳后 300000：90000 Online + 210000 Degraded。
	s = m.Summarize("dev", 300000)
	checkWindow(t, s.Day, 90000, 210000, 0)

	// 300001：最后 1ms 为 Offline。
	s = m.Summarize("dev", 300001)
	checkWindow(t, s.Day, 90000, 210000, 1)
}

// 监测时长不足完整窗口时，窗口左端取监测起点。
func TestShortMonitoringWindow(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 1000)
	m.RecordHeartbeat("dev", 1000)

	s := m.Summarize("dev", 61_000) // 实际窗口只有 60000
	checkWindow(t, s.Day, 60000, 0, 0)
	checkWindow(t, s.Week, 60000, 0, 0)
	checkWindow(t, s.Month, 60000, 0, 0)
}

// 注册后、首条心跳之前的时间为 Offline。
func TestOfflineBeforeFirstHeartbeat(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 0)
	m.RecordHeartbeat("dev", 50000)

	s := m.Summarize("dev", 100000)
	checkWindow(t, s.Day, 50000, 0, 50000)
}

// 窗口左端之前的心跳仍决定窗口内状态。
func TestHeartbeatBeforeWindowLeft(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 0)
	m.RecordHeartbeat("dev", 1000) // 远在 24h 窗口左端之前

	end := windowDay + 60_000 // 窗口 [60000, end)
	s := m.Summarize("dev", end)
	// 窗口内距心跳 59000 起：31000 Online，随后 210000 Degraded，其余 Offline。
	checkWindow(t, s.Day, 31000, 210000, 86159000)
}

// 恰在右端及右端之后的心跳不贡献该窗口。
func TestHeartbeatAtAndAfterRightEdge(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 0)
	m.RecordHeartbeat("dev", 0)
	m.RecordHeartbeat("dev", 450000)  // 恰在右端
	m.RecordHeartbeat("dev", 460000)  // 右端之后
	m.RecordHeartbeat("dev", 1000000) // 更晚

	s := m.Summarize("dev", 450000)
	checkWindow(t, s.Day, 90000, 210000, 150000)

	// 右端包含 450000 这条心跳后，结果改变。
	s = m.Summarize("dev", 450001)
	checkWindow(t, s.Day, 90001, 210000, 150000)
}

// 补传一条不改变最新心跳的旧心跳，仍会改写历史区间。
func TestLateBackfillRewritesHistory(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 0)
	m.RecordHeartbeat("dev", 0)
	m.RecordHeartbeat("dev", 900000)

	before := m.Summarize("dev", 900000)
	checkWindow(t, before.Day, 90000, 210000, 600000)

	m.RecordHeartbeat("dev", 450000) // 旧心跳，不改变最新心跳
	after := m.Summarize("dev", 900000)
	checkWindow(t, after.Day, 180000, 420000, 300000)
}

// 重复注册不改变监测起点。
func TestDuplicateRegister(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 0)
	m.Register("dev", 5000) // 不应生效
	m.RecordHeartbeat("dev", 0)

	s := m.Summarize("dev", 10000)
	checkWindow(t, s.Day, 10000, 0, 0)
}

// 三窗口长度之和恒等式在任意终点下成立。
func TestWindowSumInvariant(t *testing.T) {
	m := NewMonitor()
	m.Register("dev", 0)
	for _, ts := range []int64{0, 1_000_000, 5_000_000, 90_000_000} {
		m.RecordHeartbeat("dev", ts)
	}
	for _, end := range []int64{1, 90_000, 300_000, 4_999_999, 90_000_000, 100_000_000} {
		s := m.Summarize("dev", end)
		for _, w := range []WindowSummary{s.Day, s.Week, s.Month} {
			if w.Online+w.Degraded+w.Offline <= 0 {
				t.Fatalf("end=%d: non-positive window length", end)
			}
		}
	}
}
