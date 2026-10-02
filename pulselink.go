// Package pulselink 提供断网设备心跳历史的健康核算。
// 所有时间为已校准到同一时间轴的整数毫秒。
// 健康状态只由采样时刻决定：以任意时刻 t 之前（含 t）最后一条心跳 h 为准，
// t-h < 90000 为 Online，90000 <= t-h < 300000 为 Degraded，否则为 Offline。
// 首条心跳之前一律为 Offline。迟到的旧心跳会改写历史区间。
package pulselink

import "math/rand"

const (
	onlineLimit  int64 = 90_000  // 达到该距离转为 Degraded
	offlineLimit int64 = 300_000 // 达到该距离转为 Offline
)

const (
	windowDay   int64 = 24 * 3_600_000
	windowWeek  int64 = 7 * windowDay
	windowMonth int64 = 30 * windowDay
)

// WindowSummary 是一个窗口内三种健康状态的整数毫秒时长及在线率。
// 恒有 Online+Degraded+Offline == 实际窗口长度。
type WindowSummary struct {
	Online   int64
	Degraded int64
	Offline  int64
	Uptime   float64 // Online / 实际窗口长度
}

// Summary 是以某终点为右端的 24 小时、7 天、30 天三个窗口的核算结果。
type Summary struct {
	Day   WindowSummary
	Week  WindowSummary
	Month WindowSummary
}

// Monitor 管理多台设备的心跳历史。调用方保证串行访问。
type Monitor struct {
	devices map[string]*device
	rng     *rand.Rand
}

// NewMonitor 创建一个空的监测器。
func NewMonitor() *Monitor {
	return &Monitor{
		devices: make(map[string]*device),
		rng:     rand.New(rand.NewSource(1)),
	}
}

// Register 建立设备监测起点，不隐含心跳。重复注册同一设备不生效。
func (m *Monitor) Register(deviceID string, activeAt int64) {
	if _, ok := m.devices[deviceID]; !ok {
		m.devices[deviceID] = &device{activeAt: activeAt}
	}
}

// RecordHeartbeat 接收一条采样时刻为 sampledAt 的心跳，可乱序、可重复。
// 同设备同采样时刻的重复提交不改变结果。
func (m *Monitor) RecordHeartbeat(deviceID string, sampledAt int64) {
	d, ok := m.devices[deviceID]
	if !ok {
		return
	}
	d.addHeartbeat(sampledAt, m.rng.Uint64())
}

// Summarize 返回以 endAt 为右端的 24h/7d/30d 三个窗口核算结果。
// 窗口左端取监测起点与 endAt 减窗口长度中的较晚者，左闭右开。
func (m *Monitor) Summarize(deviceID string, endAt int64) Summary {
	d, ok := m.devices[deviceID]
	if !ok {
		return Summary{}
	}
	return Summary{
		Day:   summarizeWindow(d, endAt, windowDay),
		Week:  summarizeWindow(d, endAt, windowWeek),
		Month: summarizeWindow(d, endAt, windowMonth),
	}
}

func summarizeWindow(d *device, endAt, window int64) WindowSummary {
	lo := max(d.activeAt, endAt-window)
	a := d.rangeCount(lo, endAt)
	length := endAt - lo
	return WindowSummary{
		Online:   a.online,
		Degraded: a.degraded,
		Offline:  a.offline,
		Uptime:   float64(a.online) / float64(length),
	}
}
