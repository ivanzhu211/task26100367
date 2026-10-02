package pulselink

// agg 是某段（或若干段）相邻心跳区间的三种状态时长合计。
type agg struct {
	online   int64
	degraded int64
	offline  int64
}

func addAgg(a, b agg) agg {
	return agg{a.online + b.online, a.degraded + b.degraded, a.offline + b.offline}
}

func subAgg(a, b agg) agg {
	return agg{a.online - b.online, a.degraded - b.degraded, a.offline - b.offline}
}

// segContrib 返回从某条心跳时刻起、长度 d 的一段内三种状态的时长。
func segContrib(d int64) agg {
	a := agg{}
	a.online = min(d, onlineLimit)
	if d > onlineLimit {
		a.degraded = min(d-onlineLimit, offlineLimit-onlineLimit)
	}
	if d > offlineLimit {
		a.offline = d - offlineLimit
	}
	return a
}

// node 是笛卡尔树节点：按心跳采样时刻 ts 有序，prio 满足小根堆。
// val 为从本节点心跳到其后继心跳这一段的状态时长（末节点为 0）；
// 旋转不改变中序序列，因此除插入点的前驱外，各节点的后继关系不受旋转影响。
// sum 为子树内所有 val 的合计，用于 O(log n) 前缀和查询。
type node struct {
	ts          int64
	prio        uint64
	val         agg
	sum         agg
	left, right *node
}

func sumOf(n *node) agg {
	if n == nil {
		return agg{}
	}
	return n.sum
}

func pull(n *node) {
	n.sum = addAgg(addAgg(sumOf(n.left), n.val), sumOf(n.right))
}

func rotateRight(n *node) *node {
	l := n.left
	n.left = l.right
	l.right = n
	pull(n)
	pull(l)
	return l
}

func rotateLeft(n *node) *node {
	r := n.right
	n.right = r.left
	r.left = n
	pull(n)
	pull(r)
	return r
}

// insert 按 ts 插入新节点；已存在时不做任何修改并返回 false。
func insert(n *node, ts int64, prio uint64) (*node, bool) {
	if n == nil {
		return &node{ts: ts, prio: prio}, true
	}
	if ts == n.ts {
		return n, false
	}
	var inserted bool
	if ts < n.ts {
		n.left, inserted = insert(n.left, ts, prio)
		if inserted && n.left.prio < n.prio {
			n = rotateRight(n)
		}
	} else {
		n.right, inserted = insert(n.right, ts, prio)
		if inserted && n.right.prio < n.prio {
			n = rotateLeft(n)
		}
	}
	pull(n)
	return n, inserted
}

// updateVal 将键为 ts 的节点的 val 替换为 v，并修复路径上的聚合。
func updateVal(n *node, ts int64, v agg) {
	if n == nil {
		return
	}
	switch {
	case ts < n.ts:
		updateVal(n.left, ts, v)
	case ts > n.ts:
		updateVal(n.right, ts, v)
	default:
		n.val = v
	}
	pull(n)
}

// contains 报告键 ts 是否存在。
func contains(n *node, ts int64) bool {
	for n != nil {
		switch {
		case ts < n.ts:
			n = n.left
		case ts > n.ts:
			n = n.right
		default:
			return true
		}
	}
	return false
}

// floor 返回小于 t 的最大键及其节点；不存在时 ok 为 false。
func floor(n *node, t int64) (key int64, val agg, ok bool) {
	for n != nil {
		if n.ts < t {
			key, val, ok = n.ts, n.val, true
			n = n.right
		} else {
			n = n.left
		}
	}
	return
}

// succ 返回大于 t 的最小键；不存在时 ok 为 false。
func succ(n *node, t int64) (key int64, ok bool) {
	for n != nil {
		if n.ts > t {
			key, ok = n.ts, true
			n = n.left
		} else {
			n = n.right
		}
	}
	return
}

// minKey 返回树中最小键；空树 ok 为 false。
func minKey(n *node) (key int64, ok bool) {
	if n == nil {
		return 0, false
	}
	for n.left != nil {
		n = n.left
	}
	return n.ts, true
}

// sumLessThan 返回所有键严格小于 t 的节点 val 之和。
func sumLessThan(n *node, t int64) agg {
	total := agg{}
	for n != nil {
		if n.ts < t {
			total = addAgg(total, addAgg(sumOf(n.left), n.val))
			n = n.right
		} else {
			n = n.left
		}
	}
	return total
}

// device 保存一台设备的监测起点与按采样时刻组织的心跳集合。
type device struct {
	activeAt int64
	root     *node
	size     int
}

// addHeartbeat 插入一条心跳；重复采样时刻返回 false 且不改变任何状态。
// 只调整直接前驱与新节点的分段值，其余节点不变，摊还 O(log n)。
func (d *device) addHeartbeat(ts int64, prio uint64) bool {
	root, inserted := insert(d.root, ts, prio)
	d.root = root
	if !inserted {
		return false
	}
	d.size++
	next, hasNext := succ(d.root, ts)
	newVal := agg{}
	if hasNext {
		newVal = segContrib(next - ts)
	}
	updateVal(d.root, ts, newVal)
	if prev, _, hasPrev := floor(d.root, ts); hasPrev {
		updateVal(d.root, prev, segContrib(ts-prev))
	}
	return true
}

// cum 返回 [首条心跳, t) 内三种状态的累计时长；t 不晚于首条心跳时为零。
func (d *device) cum(t int64) agg {
	prev, prevVal, ok := floor(d.root, t)
	if !ok {
		return agg{}
	}
	total := subAgg(sumLessThan(d.root, t), prevVal)
	return addAgg(total, segContrib(t-prev))
}

// rangeCount 返回 [lo, hi) 内三种状态的时长，区间左闭右开。
// 首条心跳之前的时间计为 Offline；末条心跳之后按距离自然衰减。
func (d *device) rangeCount(lo, hi int64) agg {
	if hi <= lo {
		return agg{}
	}
	res := subAgg(d.cum(hi), d.cum(lo))
	first, ok := minKey(d.root)
	if !ok {
		res.offline += hi - lo
	} else if lo < first {
		res.offline += min(hi, first) - lo
	}
	return res
}
