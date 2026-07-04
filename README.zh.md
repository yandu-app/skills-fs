# skills-fs

将应用能力和 Agent Skills 暴露为文件的虚拟文件系统引擎。

[English](README.md)

---

skills-fs 是一个 Go 库和 CLI 工具，它将应用能力转化为虚拟文件系统。读取文件即查询数据，写入文件即执行操作，列出目录即发现可用能力。每个文件系统操作都通过 radix-tree 路由器分派到 provider（本地代码、远程 HTTP 端点或数据流），并遵循 POSIX 语义。

---

## 它做什么

AI Agent 最适合与扁平、可发现的结构交互。文件系统就是这种结构：`cat` 读取、`>` 写入、`ls` 探索。skills-fs 将任意应用能力——机器人 API、数据库查询、实时事件流——通过这些基本操作暴露出来，Agent 只需文件系统知识即可操作。

示例：NapCat QQ 机器人 → 文件系统

```bash
cat napcat/events           # 读取最近消息
echo '{"group_id":123,"message":"hello"}' > napcat/send_group  # 发送消息
cat napcat/status           # 检查机器人状态
```

---

## 概览

### 挂载类型

虚拟文件系统中的每个节点都是一个挂载条目，带有一种类型：

| 类型 | 说明 | 示例 |
|------|------|------|
| Blob | 带内联内容的静态文件，只读。 | `/SKILL.md`, `/AGENTS.md` |
| API | 内容由 provider `read` 动作产生；写入转发为 provider `write` 动作。支持 JSON payload 转发（`writeParams: "json"`）。 | `napcat/send_group`, `napcat/events` |
| Dir | 包含嵌套挂载的静态目录。 | `/napcat/`, `/napcat/groups/` |
| DynamicDir | Provider 驱动的目录。`readdir` 时调用 provider 动作，返回 JSON 条目。条目与已注册挂载匹配以确定类型。 | `groups/{group_id}/`, `friends/{user_id}/` |
| Stream | 有界环形缓冲区。支持 `block`、`drop` 或 `error` 反压策略。多个 handle 共享一个缓冲区（FIFO 语义）。 | `napcat/events`, `napcat/alerts` |
| Link | 指向其他挂载路径的符号链接。 | `/my-skill -> /skills/real-skill` |

### 主要特性

- radix-tree 路由器：快速前缀匹配，用于路径解析。
- 分片 handle：16 个 map 分片，支持并发 open/read/write。
- 建议性 flock：每条路径的共享/排他锁，关闭时自动释放。
- 串行队列：每条挂载独立序列化，防止竞态，同时保留跨路径并发。
- 写入缓冲：payload 合并，直到达到大小阈值、时间延迟或换行才触发刷新。
- 事件总线：创建/写入/删除广播，支持路径前缀过滤。
- Provider 缓存：基于 TTL 的 `(action, params)` 缓存，按挂载配置。
- Prometheus 指标：WebDAV 和 WebSocket 服务器的 `/metrics` 端点。

---

## 适配层

三种传输层让 skills-fs 触达任何客户端：

- FUSE (Linux) — 通过 `go-fuse/v2` 的原生挂载，含 inotify 转发。
- WebDAV — 完整 HTTP 服务器：Basic Auth、TLS、gzip、CORS、速率限制、ETag、Range 请求、属性缓存。
- WebSocket — 流式操作，JSON/二进制消息，per-message deflate（RFC 7692），订阅 ID 用于事件监听。

---

## Provider

- HTTP provider — 将 `Invoke` 调用作为 JSON POST 请求转发到远程 HTTP
  端点（`{ "action": "...", "params": {...} }`）。可配置重试（指数退避
  和抖动），连续失败时熔断器打开。

- Local provider — 执行进程内 Go 函数。

---

## 宿主绑定

skills-fs 通过 Go 的 cgo export 机制编译为 C 共享库（`libgobridge.so`）。
调用方使用稠密递增的 `uintptr` handle——无需管理指针。

| 绑定 | 技术 | 接口 |
|------|------|------|
| Python | ctypes | `skills_fs.py` — 面向对象包装 |
| Node.js | N-API (node-addon-api) | `index.js` — 异步方法，`path()` 调用 |
| Go | 直接导入 | `github.com/skills-fs/skills-fs/core` |

---

## Skills 系统

Skill 是一个声明式包，从模板生成文件系统挂载。Skill 定义其能力、文档和
Agent 指导——然后 skills-fs 在运行时生成实际的挂载结构。

```json
{
  "name": "napcat-cli",
  "description": "NapCat QQ 机器人消息",
  "bodyTemplate": "# NapCat CLI Skill\n\n通过文件系统访问 QQ 机器人...",
  "agentsTemplate": "# Agent 指导\n\n## 守护进程要求...",
  "exposeAtRoot": true,
  "allowedTools": ["read_file", "write_file", "list_directory"]
}
```

Skill 生成器将 `SKILL.md` 写入磁盘（YAML frontmatter + 模板内容），并可选地
生成带有 Agent 指导的 `AGENTS.md`。当 `exposeAtRoot` 为 true 时，这些文件
还会在虚拟文件系统的 `/SKILL.md` 和 `/AGENTS.md` 处挂载。

### 示例：NapCat CLI 集成

[napcat-cli](https://github.com/cyjin-yl/napcat-cli) 项目展示了完整的 skills-fs
集成：

1. 监控守护进程（`daemon/watch.py`）连接到 NapCat 的 WebSocket，将事件写入
   磁盘，并运行实现 skills-fs provider 协议的 HTTP 服务器。

2. skills-fs 配置（`skills-fs-config.json`）声明 provider URL
   （`http://127.0.0.1:18821/invoke`）并包含片段文件
   （`skills-fs-fragment.json`），其中包含 napcat-cli 的完整挂载树。

3. 片段文件定义了数十个挂载点：`napcat/send_group`、`napcat/events`、
   `napcat/groups/{group_id}/{time_range}/{message_id}` 等。动态目录让
   Agent 按群组和时间范围浏览消息历史。

4. FUSE 挂载将所有路径作为真实文件系统路径暴露给 AI Agent，Agent 仅使用
   `cat`、`ls` 和 `echo >` 进行交互。

---

## 快速开始

### Go 库

```go
import "github.com/skills-fs/skills-fs/core"

fs, _ := core.NewFileSystem(core.Config{
    MaxOpenHandles: 1024,
    DefaultUID:     1000,
    DefaultGID:     1000,
})
fs.Mount(core.MountEntry{
    Path: "/hello",
    Kind: core.KindBlob,
    Mode: 0o644,
    BlobData: []byte("world"),
})
data, _ := fs.OpenRead("/hello")
```

### 命令行

```bash
go run ./cmd/skills-fs webdav -addr :8080
go run ./cmd/skills-fs websocket -addr :8081
go run ./cmd/skills-fs fuse -mountpoint /tmp/skills-fs
go run ./cmd/skills-fs webdav -config config.json
go run ./cmd/skills-fs validate -config config.json
```

### Python

```python
from skills_fs import SkillsFs
import json

cfg = json.loads(open("config.json").read())
fs = SkillsFs(cfg)
print(fs.read("/hello"))  # b"world"
```

### Node.js

```javascript
const SkillsFs = require("skills-fs");

const fs = new SkillsFs(require("./config.json"));
console.log(fs.read("/hello").toString()); // "world"
```

---

## 配置

配置文件为 JSON，顶层键如下：

| 键 | 说明 |
|----|------|
| `providers` | provider 定义数组（id、url）。 |
| `mounts` | 挂载条目数组（path、kind、mode、read/write 动作、provider）。 |
| `skills` | Skill 定义数组。 |
| `skillsRoot` | Skill `SKILL.md` 文件的生成目录。 |
| `includes` | 要合并的附加配置文件（相对于父文件解析）。 |
| `defaultUID` / `defaultGID` | 生成挂载的默认所有权。 |
| `maxOpenHandles` | Handle 配额（默认：1024）。 |
| `lockTimeout` | 建议性锁超时（默认：30s）。 |
| `serialQueue` | 每条挂载的串行队列大小（默认：1）。 |

### 配置包含

多个 skills 可共享一个 skills-fs 实例，无需单一全局配置。`includes` 数组
加载并合并附加配置文件：

```json
{
  "skillsRoot": "$HOME/.hermes/skills",
  "providers": [{"id": "napcat", "url": "http://127.0.0.1:18821/invoke"}],
  "includes": ["skills-fs.d/napcat-cli.json"]
}
```

### 信号

- `SIGINT` / `SIGTERM`: 优雅关闭
- `SIGHUP`: 重载配置文件（webdav / websocket 命令）

### 指标

WebDAV 和 WebSocket 服务器在 `/metrics` 提供 Prometheus 文本格式指标。

---

## 开发

```bash
make all            # lint + test + vulncheck
make quick          # fmt + vet + core/registry/provider 测试（快速）
make ci             # fmt + lint + test + coverage + race + vulncheck + bench（完整）
make lint           # go vet + staticcheck
make test           # 运行所有测试
make race           # core 测试（带竞争检测器）
make coverage       # 检查 core 覆盖率（85% 门槛）
make vulncheck      # 扫描依赖漏洞
make bench          # 运行基准测试
make bench-gate     # 与基线比较基准（benchstat）
make gen-docs       # 重新生成 API 参考文档
make binding-node   # 构建 Node.js N-API addon
make binding-python # 构建 Python ctypes 模块
make clean          # 清除构建产物
```

- `core` 包：>91% 语句覆盖率；总计 >85%。
- 路由器和路径规范化的模糊测试。
- 基准测试：路径解析、stat、写入、锁争用、串行队列、事件发射、流读写、
  handle 开闭、HTTP provider 往返。

---

## 文档

### API 参考

由源代码生成，位于 [`docs/api/`](docs/api/)：

- [core](docs/api/core.md) — FileSystem、MountEntry、Handle、Config、事件、锁、流、指标、skills
- [adapter](docs/api/adapter.md) — MountOptions、adapter 接口
- [adapter/fuse](docs/api/adapter_fuse.md)
- [adapter/webdav](docs/api/adapter_webdav.md)
- [adapter/websocket](docs/api/adapter_websocket.md)
- [provider/http](docs/api/provider_http.md)
- [provider/local](docs/api/provider_local.md)
- [provider/cache](docs/api/provider_cache.md)

### 设计文档

- [开发交接](docs/DEVELOPMENT_HANDOFF.md)
- [架构](docs/ARCHITECTURE.md)
- [测试](docs/TESTING.md)
- [里程碑](docs/MILESTONES.md)
