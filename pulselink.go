// Package pulselink 提供断网设备心跳历史的健康核算。
// 所有时间为已校准到同一时间轴的整数毫秒；健康状态只由采样时刻决定，
// 迟到的旧心跳会改写历史区间，与报文接收时刻无关。
package pulselink

// 健康状态阈值（毫秒）。
const (
	onlineThreshold   = 90000  // 距最后心跳不足 90s 为 Online
	degradedThreshold = 300000 // 达到 90s 但不足 300s 为 Degraded，之后为 Offline
)

// 窗口长度（毫秒）。
const (
	Window24h = 24 * 3600 * 1000
	Window7d  = 7 * 24 * 3600 * 1000
	Window30d = 30 * 24 * 3600 * 1000
)

// WindowSummary 是一个窗口的健康核算结果，时长单位均为毫秒。
type WindowSummary struct {
	Online   int64
	Degraded int64
	Offline  int64
	Uptime   float64 // Online / 实际窗口长度
}

// Summary 是 Summarize 的返回结果，包含 24h、7d、30d 三个窗口。
type Summary struct {
	Day   WindowSummary
	Week  WindowSummary
	Month WindowSummary
}

// Monitor 管理多台设备的心跳历史。调用方保证串行访问。
type Monitor struct {
	devices map[string]*device
	rng     uint64 // splitmix64 状态，用于生成 treap 优先级
}

// NewMonitor 创建一个空的 Monitor。
func NewMonitor() *Monitor {
	return &Monitor{devices: make(map[string]*device), rng: 0x9E3779B97F4A7C15}
}

// next 生成下一个伪随机数（splitmix64）。
func (m *Monitor) next() uint64 {
	m.rng += 0x9E3779B97F4A7C15
	z := m.rng
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Register 建立设备监测起点，不隐含一条心跳。
func (m *Monitor) Register(deviceID string, activeAt int64) {
	if _, ok := m.devices[deviceID]; !ok {
		m.devices[deviceID] = &device{activeAt: activeAt}
	}
}

// RecordHeartbeat 记录一条采样时刻为 sampledAt 的心跳。
// 可任意乱序；同设备同采样时刻的重复提交不改变结果。
func (m *Monitor) RecordHeartbeat(deviceID string, sampledAt int64) {
	d, ok := m.devices[deviceID]
	if !ok {
		return
	}
	var inserted bool
	d.root, inserted = insert(d.root, sampledAt, m.next())
	if inserted {
		d.size++
	}
}

// Summarize 返回以 endAt 为右端的 24h/7d/30d 窗口核算结果。
// 窗口左端取监测起点与 endAt 减窗口长度中的较晚者，左闭右开。
func (m *Monitor) Summarize(deviceID string, endAt int64) Summary {
	d, ok := m.devices[deviceID]
	if !ok {
		return Summary{}
	}
	return Summary{
		Day:   d.window(endAt, Window24h),
		Week:  d.window(endAt, Window7d),
		Month: d.window(endAt, Window30d),
	}
}

// 累计函数：一条心跳之后 x 毫秒内各状态的累计时长。
func cumOnline(x int64) int64 { return min(x, onlineThreshold) }
func cumDegraded(x int64) int64 {
	return min(max(x-onlineThreshold, 0), degradedThreshold-onlineThreshold)
}
func cumOffline(x int64) int64 { return max(x-degradedThreshold, 0) }

// window 核算 [max(activeAt, endAt-win), endAt) 的三状态时长。
func (d *device) window(endAt, win int64) WindowSummary {
	right := endAt
	left := max(d.activeAt, endAt-win)

	// A: ts <= left；B: left < ts < right；C: ts >= right。
	a, bc := split(d.root, left+1)
	b, c := split(bc, right)
	defer func() { d.root = merge(a, merge(b, c)) }()

	var online, degraded, offline int64

	// 头段 [left, headEnd)：由不晚于 left 的最后一条心跳 p 决定。
	headEnd := right
	if b != nil {
		headEnd = b.agg.min
	}
	if a != nil {
		p := a.agg.max
		online += cumOnline(headEnd-p) - cumOnline(left-p)
		degraded += cumDegraded(headEnd-p) - cumDegraded(left-p)
		offline += cumOffline(headEnd-p) - cumOffline(left-p)
	} else {
		offline += headEnd - left // 首条心跳之前为 Offline
	}

	// 中段：窗口内部相邻心跳的完整间隔。
	if b != nil {
		online += b.agg.online
		degraded += b.agg.degraded
		offline += b.agg.offline
		// 尾段 [max, right)：由窗口内最后一条心跳决定。
		tail := right - b.agg.max
		online += cumOnline(tail)
		degraded += cumDegraded(tail)
		offline += cumOffline(tail)
	}

	length := right - left
	return WindowSummary{
		Online:   online,
		Degraded: degraded,
		Offline:  offline,
		Uptime:   float64(online) / float64(length),
	}
}
