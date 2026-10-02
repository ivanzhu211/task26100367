# pulselink

断网设备心跳历史健康核算库。健康状态只由心跳的**采样时刻**决定，
迟到的旧心跳会改写历史区间，与报文接收时刻无关。
所有时间为已校准到同一时间轴的整数毫秒；调用方保证串行访问。

## API

```go
m := pulselink.NewMonitor()
m.Register("dev-1", activeAt)        // 建立监测起点，不隐含心跳
m.RecordHeartbeat("dev-1", sampledAt) // 可乱序、可重复（重复不改变结果）
s := m.Summarize("dev-1", endAt)      // s.Day / s.Week / s.Month
```

每个窗口返回 `Online`、`Degraded`、`Offline`（整数毫秒）与 `Uptime`。
窗口左端取 `max(activeAt, endAt-windowLen)`，左闭右开；
三状态时长之和恒等于实际窗口长度。

状态定义（时刻 t 取不晚于 t 的最后心跳 h）：`t-h < 90s` 为 Online，
`90s <= t-h < 300s` 为 Degraded，`t-h >= 300s` 为 Offline；首条心跳之前为 Offline。

## 实现与复杂度

每台设备的心跳存于一棵按采样时刻排序的**随机 treap**，节点维护子树聚合：
相邻心跳间隔产生的三状态时长之和（`online/degraded/offline`）、`count`、`min`、`max`。
间隔 `g` 的贡献只取决于 `g` 本身：`min(g,90s)`、`clamp(g-90s,0,210s)`、`max(g-300s,0)`，
因此合并两棵子树只需补上接缝间隔的贡献。

- `RecordHeartbeat`：两次 split + 两次 merge，期望 O(log n)；重复时间戳原样合并回去，幂等。
- `Summarize`（单窗口）：按 `left+1`、`right` 两次 split 取出窗口内心跳子树，
  头段 `[left, 窗口内首心跳)` 由 `<= left` 的最后心跳经累计函数
  `cumOnline/cumDegraded/cumOffline` 作差求得，中段直接读子树聚合，
  尾段 `[窗口内末心跳, right)` 同样由累计函数求得，再 merge 复原。期望 O(log n)。
- 空间：每个不同心跳一个节点（约 64 字节），随设备数与心跳数线性增长；
  不展开时间轴、不分桶、不丢弃迟到心跳。

## 测试

```sh
go test ./...          # 全部正确性测试 + 压力测试
go test -short ./...   # 跳过压力测试
go test -run TestStress -v ./...  # 查看耗时与内存
```

- 题目给定用例（0/400000 后补传 200000）、90s/300s 精确边界、
  不足完整窗口、窗口左端之前/恰在右端/右端之后的心跳。
- 独立慢速参考实现（O(n) 扫描）对随机乱序、重复、穿插查询逐次核对，
  另有大时间尺度版本覆盖三个窗口的左端裁剪。
- 压力测试：100 万条不同心跳（约 10% 为 400s~700s 大空档）随机乱序到达，
  随后 1 万个不同终点查询并逐次校验时长和不变式。
  参考数据（Apple Silicon）：插入约 2.7µs/条，查询约 13µs/次（3 个窗口），
  保留堆内存约 64 字节/心跳。
