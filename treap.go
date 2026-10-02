package pulselink

// device 保存单台设备的监测起点与心跳索引（按采样时刻排序的 treap）。
type device struct {
	activeAt int64
	root     *node
	size     int
}

// node 是 treap 节点，维护子树聚合以便 O(log n) 区间核算。
type node struct {
	ts   int64
	prio uint64
	l, r *node
	agg  agg
}

// agg 是子树聚合：按中序（时间序）排列的心跳，
// online/degraded/offline 为子树内部相邻心跳间隔产生的三状态时长之和。
type agg struct {
	count    int
	min, max int64
	online   int64
	degraded int64
	offline  int64
}

// gapDurations 计算一条心跳之后 gap 毫秒内的三状态时长：
// [0,90000) Online，[90000,300000) Degraded，[300000,inf) Offline。
func gapDurations(gap int64) (online, degraded, offline int64) {
	online = min(gap, onlineThreshold)
	degraded = min(max(gap-onlineThreshold, 0), degradedThreshold-onlineThreshold)
	offline = max(gap-degradedThreshold, 0)
	return
}

// leaf 构造单节点聚合。
func leaf(ts int64) agg {
	return agg{count: 1, min: ts, max: ts}
}

// combine 合并两个中序相邻的聚合（a 的键全部小于 b）。
func combine(a, b agg) agg {
	if a.count == 0 {
		return b
	}
	if b.count == 0 {
		return a
	}
	on, deg, off := gapDurations(b.min - a.max)
	return agg{
		count:    a.count + b.count,
		min:      a.min,
		max:      b.max,
		online:   a.online + b.online + on,
		degraded: a.degraded + b.degraded + deg,
		offline:  a.offline + b.offline + off,
	}
}

func aggOf(n *node) agg {
	if n == nil {
		return agg{}
	}
	return n.agg
}

func (n *node) pull() {
	n.agg = combine(combine(aggOf(n.l), leaf(n.ts)), aggOf(n.r))
}

// split 将 t 按键分为 (< key, >= key) 两棵 treap。
func split(t *node, key int64) (lo, hi *node) {
	if t == nil {
		return nil, nil
	}
	if t.ts < key {
		l, r := split(t.r, key)
		t.r = l
		t.pull()
		return t, r
	}
	l, r := split(t.l, key)
	t.l = r
	t.pull()
	return l, t
}

// merge 合并两棵 treap，要求 a 的键全部小于 b。
func merge(a, b *node) *node {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if a.prio < b.prio {
		a.r = merge(a.r, b)
		a.pull()
		return a
	}
	b.l = merge(a, b.l)
	b.pull()
	return b
}

// insert 插入 ts；若已存在则不改变结构并返回原树与 false。
func insert(root *node, ts int64, prio uint64) (*node, bool) {
	a, bc := split(root, ts)
	b, c := split(bc, ts+1)
	if b != nil {
		return merge(a, merge(b, c)), false
	}
	n := &node{ts: ts, prio: prio, agg: leaf(ts)}
	return merge(merge(a, n), c), true
}
