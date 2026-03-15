# github.com/soulteary/gorge-search

Go 全文搜索代理服务，替代 PHP 直接与 Elasticsearch 交互的方式。

## 功能

- **文档索引** — 接收 PHP 侧的文档数据并写入 Elasticsearch
- **全文搜索** — 构建 Elasticsearch DSL 查询，返回匹配的 PHID 列表
- **索引管理** — 创建/重建索引、检查索引状态、获取统计信息
- **多后端支持** — 支持配置多个 Elasticsearch 集群，按角色（read/write）分流
- **健康检查** — 自动标记不健康的后端节点并跳过

## API

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/healthz` | 健康检查 |
| POST | `/api/search/index` | 索引文档 |
| POST | `/api/search/query` | 执行搜索 |
| POST | `/api/search/init` | 初始化索引 |
| GET | `/api/search/exists` | 检查索引是否存在 |
| GET | `/api/search/stats` | 获取索引统计 |
| GET | `/api/search/sane` | 检查索引是否正常 |
| GET | `/api/search/backends` | 列出后端配置 |

## 配置

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `LISTEN_ADDR` | `:8120` | 监听地址 |
| `SERVICE_TOKEN` | (空) | 服务鉴权 Token |
| `ES_HOST` | (空) | Elasticsearch 地址（逗号分隔） |
| `ES_INDEX` | `phabricator` | 索引名 |
| `ES_VERSION` | `5` | Elasticsearch 主版本号 |
| `ES_TIMEOUT` | `15` | 请求超时秒数 |
| `ES_PROTOCOL` | `http` | 协议 |
| `SEARCH_BACKENDS` | (空) | JSON 格式后端配置（覆盖 ES_* 变量） |

### JSON 配置

通过 `SEARCH_CONFIG_FILE` 指定配置文件路径：

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
    }
  ]
}
```

## 运行

```bash
ES_HOST=localhost:9200 go run ./cmd/server
```
