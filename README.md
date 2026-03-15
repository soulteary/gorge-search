# gorge-search

Go 全文搜索代理服务，为 Phorge 提供统一的全文搜索 HTTP API。作为 PHP 应用与搜索引擎之间的代理层，将 Phorge 的文档索引和查询请求转发到 Elasticsearch 或 Meilisearch 后端，支持多后端读写分离和自动故障转移。

## 特性

- 文档索引，接收 Phorge PHP 端的文档数据写入搜索后端
- 全文搜索，构建搜索引擎 DSL 查询，返回匹配的 PHID 列表
- 索引管理，创建/重建索引、检查索引状态、获取统计信息
- 多后端支持，Elasticsearch 和 Meilisearch 双引擎，按角色（read/write）分流
- 多主机负载均衡，Elasticsearch 后端支持多主机随机选取
- 健康检查，自动标记不健康的后端节点并跳过，请求成功后恢复
- 双重配置模式：环境变量和 JSON 配置文件
- 统一 JSON 响应格式与错误码体系
- 静态编译，Docker 镜像极轻量
- 内置健康检查端点，适配容器编排

## 快速开始

### 本地运行

```bash
go build -o gorge-search ./cmd/server
ES_HOST=localhost:9200 ./gorge-search
```

服务默认监听 `:8120`。

### Docker 运行

```bash
docker build -t gorge-search .
docker run -p 8120:8120 -e ES_HOST=elasticsearch:9200 gorge-search
```

### 使用 Meilisearch 后端

```bash
export SEARCH_ENGINE=meilisearch
export MEILI_HOST=localhost:7700
export MEILI_MASTER_KEY=your_master_key
./gorge-search
```

## 配置

支持两种配置方式：环境变量（默认）和 JSON 配置文件。

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `:8120` | 服务监听地址 |
| `SERVICE_TOKEN` | (空) | API 认证 Token，通过 `X-Service-Token` 请求头传递 |
| `SEARCH_CONFIG_FILE` | (空) | JSON 配置文件路径，设置后从文件加载配置 |
| `SEARCH_BACKENDS` | (空) | JSON 格式后端配置数组（覆盖单引擎环境变量） |
| `SEARCH_ENGINE` | (空) | 搜索引擎类型，`meilisearch` 或留空使用 Elasticsearch |

#### Elasticsearch 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ES_HOST` | (空) | Elasticsearch 地址，逗号分隔多个主机 |
| `ES_INDEX` | `phabricator` | 索引名 |
| `ES_VERSION` | `5` | Elasticsearch 主版本号 |
| `ES_TIMEOUT` | `15` | 请求超时秒数 |
| `ES_PROTOCOL` | `http` | 协议（`http` / `https`） |

#### Meilisearch 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MEILI_HOST` | (空) | Meilisearch 地址 |
| `MEILI_INDEX` | `phabricator` | 索引名 |
| `MEILI_MASTER_KEY` | (空) | API Key |
| `MEILI_TIMEOUT` | `15` | 请求超时秒数 |
| `MEILI_PROTOCOL` | `http` | 协议 |

### JSON 配置文件

当设置 `SEARCH_CONFIG_FILE` 时，从指定的 JSON 文件加载配置：

```json
{
  "listenAddr": ":8120",
  "serviceToken": "phorge-internal-token",
  "backends": [
    {
      "type": "elasticsearch",
      "hosts": ["es1:9200", "es2:9200"],
      "index": "phabricator",
      "version": 5,
      "timeout": 15,
      "roles": ["read", "write"]
    },
    {
      "type": "meilisearch",
      "hosts": ["meilisearch:7700"],
      "index": "phabricator",
      "apiKey": "master-key",
      "roles": ["read", "write"]
    }
  ]
}
```

## API

所有 `/api/search/*` 端点在配置 `SERVICE_TOKEN` 时需要认证。认证方式：

- 请求头：`X-Service-Token: <token>`
- 查询参数：`?token=<token>`

### POST /api/search/index

索引文档到搜索后端。

**请求**：

```json
{
  "phid": "PHID-TASK-abc123",
  "type": "TASK",
  "title": "Fix login bug",
  "dateCreated": 1700000000,
  "dateModified": 1700001000,
  "fields": [
    {"name": "body", "corpus": "Users cannot login with SSO"}
  ],
  "relationships": [
    {"name": "auth", "relatedPHID": "PHID-USER-xyz", "rtype": "auth"}
  ]
}
```

**响应** (200)：

```json
{"data": {"phid": "PHID-TASK-abc123", "status": "indexed"}}
```

### POST /api/search/query

执行全文搜索，返回匹配的 PHID 列表。

**请求**：

```json
{
  "query": "login bug",
  "types": ["TASK"],
  "authorPHIDs": ["PHID-USER-xyz"],
  "limit": 20,
  "offset": 0
}
```

**响应** (200)：

```json
{"data": {"phids": ["PHID-TASK-abc123"], "count": 1}}
```

### POST /api/search/init

初始化搜索索引（会删除并重建）。

**请求**：

```json
{"docTypes": ["TASK", "DREV", "WIKI"]}
```

**响应** (200)：

```json
{"data": {"status": "initialized"}}
```

### GET /api/search/exists

检查索引是否存在。

**响应** (200)：

```json
{"data": {"exists": true}}
```

### GET /api/search/stats

获取索引统计信息。

**响应** (200)：

```json
{"data": {"queries": 1500, "documents": 30000, "deleted": 50, "storage_bytes": 52428800}}
```

### POST /api/search/sane

检查索引配置是否与预期一致。

**请求**：

```json
{"docTypes": ["TASK", "DREV"]}
```

**响应** (200)：

```json
{"data": {"sane": true}}
```

### GET /api/search/backends

列出已配置的搜索后端信息。

**响应** (200)：

```json
{"data": [{"type": "elasticsearch", "hosts": ["es:9200"], "index": "phabricator", "version": 5, "roles": ["read", "write"]}]}
```

### GET /healthz

健康检查端点，不需要认证。

**响应** (200)：

```json
{"status": "ok"}
```

### 错误响应

所有错误响应使用统一的 JSON 格式：

```json
{
  "error": {
    "code": "ERR_SEARCH_FAILED",
    "message": "all fulltext search backends failed: ..."
  }
}
```

| 错误码 | HTTP 状态码 | 含义 |
|---|---|---|
| `ERR_UNAUTHORIZED` | 401 | Service Token 缺失或无效 |
| `ERR_BAD_REQUEST` | 400 | 请求参数缺失或格式错误 |
| `ERR_INDEX_FAILED` | 502 | 文档索引失败 |
| `ERR_SEARCH_FAILED` | 502 | 搜索执行失败 |
| `ERR_INIT_FAILED` | 502 | 索引初始化失败 |
| `ERR_CHECK_FAILED` | 502 | 索引检查失败 |
| `ERR_STATS_FAILED` | 502 | 统计查询失败 |

## 项目结构

```
gorge-search/
├── cmd/server/main.go                          # 服务入口，配置加载与后端初始化
├── internal/
│   ├── config/
│   │   ├── config.go                           # 配置加载（JSON 文件 / 环境变量 / JSON 环境变量）
│   │   └── config_test.go                      # 配置解析测试
│   ├── engine/
│   │   ├── backend.go                          # SearchBackend 接口定义
│   │   ├── model.go                            # Document、SearchQuery 数据模型
│   │   ├── engine.go                           # 搜索引擎调度器：角色分发、故障转移
│   │   ├── engine_test.go                      # 引擎调度测试（mock 后端、failover）
│   │   ├── elasticsearch/
│   │   │   └── backend.go                      # Elasticsearch 后端：多主机健康管理、DSL 构建、索引配置
│   │   └── meilisearch/
│   │       └── backend.go                      # Meilisearch 后端：概念映射、过滤语法、设置管理
│   ├── esquery/
│   │   └── builder.go                          # Elasticsearch Bool Query 构建器、Phorge 字段/关系常量
│   └── httpapi/
│       ├── handlers.go                         # HTTP 路由注册、认证中间件与请求处理
│       └── handlers_test.go                    # API 端到端测试
├── Dockerfile                                  # 多阶段 Docker 构建
├── go.mod
└── go.sum
```

## 开发

```bash
# 运行全部测试
go test ./...

# 运行测试（带详细输出）
go test -v ./...

# 构建二进制
go build -o gorge-search ./cmd/server
```

## 技术栈

- **语言**：Go 1.26
- **HTTP 框架**：[Echo](https://echo.labstack.com/) v4.15.1
- **许可证**：Apache License 2.0
