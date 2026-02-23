# Argus

> **Argus**（阿耳戈斯）— 希腊神话中的百眼巨人，能同时监视一切。

轻量级日志采集与告警系统，零外部依赖，单二进制部署。

## 功能特性

- **多源日志采集** — 支持本地文件（tail + fsnotify）和 Docker 容器日志（Engine API）
- **自研存储引擎** — WAL 持久化 + 内存环形缓冲区 + 倒排索引，无需 Elasticsearch / MySQL
- **实时告警** — ERROR/FATAL 日志自动推送飞书群通知，支持签名校验、指数退避重试
- **智能去重与限流** — 指纹去重 + 冷却限流 + inflight 追踪，避免告警风暴
- **全文搜索** — 倒排索引 + 两段式搜索（索引 + tail-scan），毫秒级响应
- **Web 管理界面** — 日志查看、筛选、分页、统计面板，内嵌静态文件无需前端构建
- **热加载** — `sources` 配置变更自动生效，无需重启
- **崩溃恢复** — WAL + Checkpoint 机制，重启后自动恢复状态

## 快速开始

### 构建

```bash
go build -o argus .
```

### 配置

复制示例配置并编辑：

```bash
cp config.example.yaml config.yaml
```

```yaml
sources:
  - type: file
    path: /var/log/app/order-api.log
  - type: docker
    container: my-api

storage:
  max_entries: 50000
  data_dir: ./data
  wal_compact_threshold: 60000
  checkpoint_interval: 5s
  index_rebuild_interval: 10000
  index_rebuild_max_interval: 30s

alert:
  feishu:
    webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/your-hook-id"
    secret: "your-signing-secret"
  cooldown: 60s
  max_retries: 5

server:
  addr: ":8080"
  base_url: "http://your-server:8080"

auth:
  username: admin
  password_hash: "$2a$10$..."  # bcrypt hash
  jwt_secret: ""               # 留空则自动生成
```

生成密码哈希：

```bash
htpasswd -nbBC 10 "" your-password | cut -d: -f2
```

### 运行

```bash
./argus -config config.yaml
```

访问 `http://localhost:8080/login` 进入管理界面。

## 日志格式

Argus 要求日志为 **JSON Lines** 格式（每行一条合法 JSON）：

```json
{
  "timestamp": "2026-02-22T10:30:00.123+08:00",
  "level": "ERROR",
  "service": "order-api",
  "message": "failed to connect database",
  "trace_id": "a1b2c3d4e5f6",
  "caller": "db/connection.go:42",
  "stack_trace": "goroutine 1 [running]:\nmain.main()\n\t/app/main.go:10",
  "extra": { "user_id": 10086, "latency_ms": 320 }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `timestamp` | string (ISO8601) | ✅ | 带时区，精确到毫秒 |
| `level` | string | ✅ | `DEBUG` / `INFO` / `WARN` / `ERROR` / `FATAL` |
| `service` | string | ✅ | 服务名 |
| `message` | string | ✅ | 日志内容 |
| `trace_id` | string | ❌ | 链路追踪 ID |
| `caller` | string | ❌ | 调用位置 `file:line` |
| `stack_trace` | string | ❌ | 堆栈信息，换行符转义为 `\n` |
| `extra` | object | ❌ | 自由扩展字段 |

## 架构概览

```
日志源 (文件/Docker)
    │
    ▼
Channel (cap=2048)
    │
    ▼
Pipeline (单 goroutine 串行写)
    ├── WAL (磁盘持久化, append-only, CRC32 校验)
    ├── RingBuffer (内存环形缓冲区, cap=50000)
    ├── 倒排索引 (异步重建, atomic swap)
    └── 告警检查 → Alert Worker → 飞书 Webhook
                                      │
HTTP Server ◄──── RingBuffer + Index ──┘
    ├── /admin          日志管理界面
    ├── /admin/log      日志详情页
    ├── /api/logs       日志查询 API
    ├── /api/stats      系统指标 API
    └── /healthz        健康检查
```

## Web 界面

- **日志列表** — 按时间倒序展示，支持按关键词、级别、来源、时间范围筛选
- **日志详情** — 点击展开完整信息，包括 Trace ID、Caller、Stack Trace、Extra
- **统计面板** — 缓冲区用量、摄入统计、WAL 状态、告警统计、级别/来源分布图
- **自动刷新** — 可开启定时拉取最新日志
- **游标分页** — 基于 SeqID 的高效分页

## 告警系统

当检测到 `ERROR` 或 `FATAL` 级别日志时，自动发送飞书卡片消息：

- **签名校验** — 支持飞书 HMAC-SHA256 签名验证
- **去重机制** — 基于日志指纹（service + message hash），避免重复告警
- **冷却限流** — 同一告警在冷却期内不重复发送（默认 60s）
- **指数退避** — 发送失败后 1s → 2s → 4s → 8s → 16s 重试
- **详情跳转** — 卡片内置"查看日志详情"按钮，一键跳转 Web 详情页

## 项目结构

```
argus/
├── main.go                     # 入口，组装各模块
├── config.example.yaml         # 示例配置文件
├── Dockerfile                  # 多阶段构建
├── internal/
│   ├── config/                 # 配置加载与校验
│   ├── tailer/                 # 日志采集 (文件 + Docker)
│   ├── parser/                 # JSON 解析与 Schema 校验
│   ├── pipeline/               # 核心处理管道
│   ├── storage/
│   │   ├── ringbuffer.go       # 环形缓冲区
│   │   ├── wal.go              # WAL 读写与 Compaction
│   │   ├── index.go            # 倒排索引
│   │   └── checkpoint.go       # 状态持久化
│   ├── alert/
│   │   ├── feishu.go           # 飞书 Webhook 通知
│   │   └── limiter.go          # 去重与限流
│   ├── server/                 # HTTP 路由、鉴权、API
│   └── model/                  # 数据模型定义
├── web/
│   ├── templates/              # HTML 模板
│   └── static/                 # CSS + JS
├── scripts/
│   └── generate_test_logs.go   # 测试日志生成工具
└── data/                       # 运行时数据 (WAL, Checkpoint)
```

## 部署

### systemd

```ini
[Unit]
Description=Argus Log Collector
After=network.target

[Service]
Type=simple
User=argus
ExecStart=/opt/argus/argus -config /opt/argus/config.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### Docker

```bash
# 构建镜像
docker build -t argus .

# 运行
docker run -d \
  -v /path/to/config.yaml:/app/config.yaml:ro \
  -v /data/argus:/app/data \
  -v /var/log/app:/var/log/app:ro \
  -p 8080:8080 \
  argus
```

采集 Docker 容器日志时需额外挂载：

```bash
-v /var/run/docker.sock:/var/run/docker.sock
```

## 性能指标

| 指标 | 数值 |
|------|------|
| 内存占用 | ~30-40 MB |
| 磁盘占用 | ≤ ~30 MB (WAL) |
| 写入吞吐 | > 10,000 条/秒 |
| 搜索延迟 | < 10 ms (索引命中) |
| 启动恢复 | < 1 s (5 万条) |

## 测试

```bash
go test ./...
```

生成测试日志：

```bash
go run scripts/generate_test_logs.go
```

## License

MIT
