# gorge-search 技术报告

## 1. 概述

gorge-search 是 Gorge 平台中的全文搜索代理微服务，为 Phorge（Phabricator 社区维护分支）提供统一的全文搜索 HTTP API。

该服务的核心目标是替代 Phorge PHP 端直接与 Elasticsearch 交互的方式。Phorge 的全文搜索功能原本由 PHP 类 `PhabricatorElasticFulltextStorageEngine` 直接调用 Elasticsearch REST API，每次搜索请求都从 PHP 进程发起 HTTP 调用。gorge-search 将这一功能抽取为独立的 Go HTTP 服务，作为 PHP 应用与搜索引擎之间的代理层，同时引入了多后端支持（Elasticsearch + Meilisearch）、读写分离、多主机故障转移等企业级特性。

## 2. 设计动机

### 2.1 原有方案的问题

Phorge 的全文搜索直接嵌入在 PHP 应用中：

1. **单引擎绑定**：PHP 端的 `PhabricatorElasticFulltextStorageEngine` 直接硬编码了 Elasticsearch 的 DSL 查询构建逻辑，无法切换到其他搜索引擎（如 Meilisearch）。更换引擎需要修改 Phorge 核心代码。
2. **无故障转移**：PHP 端配置单一 Elasticsearch 地址，该节点不可用时搜索功能完全中断。PHP 的请求-响应模型不适合维护后端健康状态——每次请求都是无状态的，无法在请求间记录节点健康信息。
3. **无读写分离**：所有搜索和索引操作都发往同一个 Elasticsearch 端点，无法将读请求分流到 replica 节点以减轻 master 压力。
4. **进程模型限制**：PHP-FPM 的进程模型中，每个请求独立建立和销毁 HTTP 连接，无法复用到 Elasticsearch 的长连接。高并发搜索场景下产生大量短连接，增加 Elasticsearch 的连接管理开销。
5. **版本适配困难**：Elasticsearch 的 API 在 v2、v5、v7 之间存在破坏性变更（字段类型名、映射结构、查询语法），PHP 端需要在同一份代码中维护多个版本的适配分支，增加了维护负担。

### 2.2 gorge-search 的解决思路

将全文搜索功能抽取为独立的 Go HTTP 代理服务：

- **多引擎抽象**：通过 `SearchBackend` 接口统一 Elasticsearch 和 Meilisearch 的操作，新增搜索引擎只需实现接口，无需修改上层代码。
- **读写分离 + 故障转移**：每个后端配置独立的角色（read/write），写操作广播到所有 write 后端，读操作自动 failover 到下一个 read 后端。
- **多主机健康管理**：Go 常驻进程维护每个主机的健康状态，请求失败自动标记不健康并跳过，请求成功后自动恢复。
- **连接复用**：Go 的 `net/http.Client` 内置连接池，自动复用到搜索引擎的 TCP 连接，消除 PHP 端的短连接问题。
- **部署一致**：与 Gorge 平台的其他 Go 微服务保持相同的构建和部署模式——多阶段 Docker 构建、Alpine 运行镜像、健康检查端点。
- **零 SDK 依赖**：不依赖任何 Elasticsearch 或 Meilisearch 的官方 SDK，使用标准库 `net/http` 直接调用 REST API，减少依赖链和版本兼容问题。

## 3. 系统架构

### 3.1 在 Gorge 平台中的位置

```
┌──────────────────────────────────────────────────┐
│                   Gorge 平台                      │
│                                                   │
│  ┌──────────┐                                     │
│  │  Phorge  │──── HTTP POST/GET ──┐               │
│  │  (PHP)   │                     │               │
│  └──────────┘                     ▼               │
│                     ┌───────────────────────┐     │
│                     │    gorge-search       │     │
│                     │    :8120              │     │
│                     │                       │     │
│                     │    Token Auth         │     │
│                     │    Engine Dispatch    │     │
│                     │    Role Routing       │     │
│                     │    Health Tracking    │     │
│                     └──────────┬────────────┘     │
│                                │                  │
│                     ┌──────────┴──────────┐       │
│                     │                     │       │
│                     ▼                     ▼       │
│          ┌──────────────────┐  ┌─────────────────┐│
│          │  Elasticsearch   │  │  Meilisearch    ││
│          │  (多主机集群)     │  │  (单节点)        ││
│          └──────────────────┘  └─────────────────┘│
└──────────────────────────────────────────────────┘
```

### 3.2 模块划分

项目采用 Go 标准布局，分为四个内部模块：

| 模块 | 路径 | 职责 |
|---|---|---|
| config | `internal/config/` | 配置加载（JSON 文件、JSON 环境变量、独立环境变量） |
| engine | `internal/engine/` | 搜索引擎调度器、后端接口、数据模型 |
| esquery | `internal/esquery/` | Elasticsearch Bool Query 构建器、Phorge 字段/关系常量 |
| httpapi | `internal/httpapi/` | HTTP 路由注册、认证中间件与请求处理 |

入口程序 `cmd/server/main.go` 负责串联各模块：加载配置 → 构建后端实例 → 创建搜索引擎 → 注册路由 → 启动 HTTP 服务。

### 3.3 请求处理流水线

一个搜索请求经过的完整处理链路：

```
PHP 端 POST /api/search/query
       │
       ▼
┌─ Echo 框架层 ─────────────────────────────────┐
│  RequestLogger    记录请求日志                   │
│       │                                        │
│       ▼                                        │
│  Recover          捕获 panic，防止进程崩溃        │
└───────┼────────────────────────────────────────┘
        │
        ▼
┌─ 路由组 /api/search ──────────────────────────┐
│  tokenAuth        校验 X-Service-Token         │
│       │                                        │
│       ▼                                        │
│  searchQuery()    解析 SearchQuery JSON         │
│       │                                        │
│       ▼                                        │
│  Engine.Search()  按角色分发到后端               │
│       │                                        │
│       ├── Backend A (read) ── 失败 ── 跳过     │
│       │                                        │
│       └── Backend B (read) ── 成功 ── 返回     │
│                                                │
│  返回 {"data": {"phids": [...], "count": N}}   │
└────────────────────────────────────────────────┘
```

## 4. 核心实现分析

### 4.1 配置系统（internal/config）

#### 4.1.1 数据结构

```go
type Config struct {
    ListenAddr   string       `json:"listenAddr"`
    ServiceToken string       `json:"serviceToken"`
    Backends     []BackendDef `json:"backends"`
}

type BackendDef struct {
    Type     string   `json:"type"`
    Hosts    []string `json:"hosts"`
    Index    string   `json:"index"`
    Version  int      `json:"version"`
    Roles    []string `json:"roles"`
    Timeout  int      `json:"timeout"`
    Protocol string   `json:"protocol"`
    APIKey   string   `json:"apiKey,omitempty"`
}
```

`Config` 作为顶层配置容器，持有服务监听地址、认证 Token 和后端定义数组。`BackendDef` 是后端的统一描述——无论 Elasticsearch 还是 Meilisearch，都通过相同的结构体定义，由 `Type` 字段区分引擎类型。

`APIKey` 字段使用 `omitempty` 标签——Elasticsearch 不需要 API Key（通常通过网络隔离保证安全），只有 Meilisearch 需要。这种设计让两种引擎共享同一个配置结构体，避免了为每种引擎定义独立的配置类型。

#### 4.1.2 三级配置优先级

配置加载遵循三级优先级，从高到低：

**第一级：JSON 配置文件**

```go
if path := os.Getenv("SEARCH_CONFIG_FILE"); path != "" {
    cfg, err = config.LoadFromFile(path)
}
```

当设置 `SEARCH_CONFIG_FILE` 环境变量时，从指定路径读取 JSON 文件。文件内容直接反序列化为 `Config` 结构体，支持完整的多后端配置。适合生产环境——配置文件可以版本控制，便于审计和回滚。

**第二级：JSON 环境变量**

```go
if raw := os.Getenv("SEARCH_BACKENDS"); raw != "" {
    _ = json.Unmarshal([]byte(raw), &cfg.Backends)
}
```

通过 `SEARCH_BACKENDS` 环境变量传入完整的后端 JSON 数组。适合容器编排（Kubernetes ConfigMap、Docker Compose 环境变量）——无需挂载配置文件即可定义复杂的多后端配置。

**第三级：独立环境变量**

```go
func buildBackendFromEnv() []BackendDef {
    backendType := envStr("SEARCH_ENGINE", "")
    switch backendType {
    case "meilisearch":
        return buildMeilisearchFromEnv()
    default:
        return buildElasticsearchFromEnv()
    }
}
```

通过 `SEARCH_ENGINE` 环境变量选择引擎类型，再从对应的环境变量组（`ES_*` 或 `MEILI_*`）构建单个后端。适合开发环境和简单部署——无需了解 JSON 配置格式，设置几个环境变量即可运行。

`SEARCH_ENGINE` 默认为空，走 Elasticsearch 分支。这确保了向后兼容——已有的 `ES_HOST` 等环境变量配置无需任何修改即可继续工作。

#### 4.1.3 Elasticsearch 主机解析

```go
func buildElasticsearchFromEnv() []BackendDef {
    host := envStr("ES_HOST", "")
    if host == "" {
        return nil
    }
    hosts := strings.Split(host, ",")
    for i := range hosts {
        hosts[i] = strings.TrimSpace(hosts[i])
    }
    return []BackendDef{
        {
            Type:  "elasticsearch",
            Hosts: hosts,
            Roles: []string{"read", "write"},
        },
    }
}
```

`ES_HOST` 支持逗号分隔的多主机地址（如 `es1:9200,es2:9200`），`TrimSpace` 容忍地址间的空格。默认分配 `read` 和 `write` 双角色——单后端场景下不需要角色分离。当 `ES_HOST` 为空时返回 `nil`，表示无可用后端。

### 4.2 搜索引擎调度器（internal/engine）

#### 4.2.1 后端接口

```go
type SearchBackend interface {
    Type() string
    HasRole(role string) bool

    IndexDocument(doc *Document) error
    Search(q *SearchQuery) ([]string, error)
    IndexExists() (bool, error)
    InitIndex(docTypes []string) error
    IndexStats() (map[string]any, error)
    IndexIsSane(docTypes []string) (bool, error)

    Info() map[string]any
}
```

`SearchBackend` 是整个系统的核心抽象。接口方法分为三组：

**元信息**：`Type()` 返回引擎类型字符串（`"elasticsearch"` / `"meilisearch"`），`HasRole()` 检查后端是否拥有指定角色，`Info()` 返回后端的描述信息用于 API 展示。

**文档操作**：`IndexDocument()` 写入单个文档，`Search()` 执行查询返回 PHID 列表。这是最频繁调用的两个方法，对应 Phorge 的文档索引和搜索请求。

**索引管理**：`IndexExists()` 检查索引是否存在，`InitIndex()` 创建或重建索引，`IndexStats()` 获取统计信息，`IndexIsSane()` 验证索引配置是否与预期一致。这些方法对应 Phorge 的 `bin/search init` 和管理面板中的搜索状态检查。

所有方法的返回值使用通用类型（`string`、`[]string`、`map[string]any`、`bool`），不暴露引擎特有的数据结构。这确保了上层代码（engine、httpapi）完全不感知底层引擎的差异。

#### 4.2.2 数据模型

```go
type Document struct {
    PHID          string             `json:"phid"`
    Type          string             `json:"type"`
    Title         string             `json:"title"`
    DateCreated   int64              `json:"dateCreated"`
    DateModified  int64              `json:"dateModified"`
    Fields        []DocumentField    `json:"fields,omitempty"`
    Relationships []DocumentRelation `json:"relationships,omitempty"`
}
```

`Document` 直接对应 Phorge PHP 端的 `PhabricatorSearchAbstractDocument`。Phorge 的文档模型由三部分组成：

- **基础属性**：PHID（全局唯一标识符）、Type（文档类型如 TASK/DREV/WIKI）、Title、时间戳。
- **字段（Fields）**：对应 `getFieldData()` 返回的 `(field_name, corpus, aux)` 三元组。`corpus` 是可搜索的文本内容，`aux` 是辅助数据（如标签）。
- **关系（Relationships）**：对应 `getRelationshipData()` 返回的 `(field_name, related_phid, rtype, time)` 四元组。关系连接文档与其他对象（作者、项目、仓库等）。

```go
type SearchQuery struct {
    Query           string   `json:"query"`
    Types           []string `json:"types,omitempty"`
    AuthorPHIDs     []string `json:"authorPHIDs,omitempty"`
    OwnerPHIDs      []string `json:"ownerPHIDs,omitempty"`
    SubscriberPHIDs []string `json:"subscriberPHIDs,omitempty"`
    ProjectPHIDs    []string `json:"projectPHIDs,omitempty"`
    RepositoryPHIDs []string `json:"repositoryPHIDs,omitempty"`
    Statuses        []string `json:"statuses,omitempty"`
    WithAnyOwner    bool     `json:"withAnyOwner,omitempty"`
    WithUnowned     bool     `json:"withUnowned,omitempty"`
    Exclude         string   `json:"exclude,omitempty"`
    Offset          int      `json:"offset,omitempty"`
    Limit           int      `json:"limit,omitempty"`
}
```

`SearchQuery` 对应 Phorge 的 `PhabricatorSavedQuery`，覆盖了 Phorge 全文搜索的所有查询维度：关键词搜索、文档类型过滤、关系过滤（作者/所有者/订阅者/项目/仓库）、状态过滤、排除特定文档、分页参数。

#### 4.2.3 调度策略

`SearchEngine` 管理多个后端，根据操作类型按角色分发请求：

**写操作——广播策略**

```go
func (se *SearchEngine) IndexDocument(doc *Document) error {
    var lastErr error
    for _, b := range se.backends {
        if !b.HasRole("write") {
            continue
        }
        if err := b.IndexDocument(doc); err != nil {
            lastErr = err
            continue
        }
    }
    return lastErr
}
```

写操作遍历所有拥有 `write` 角色的后端，逐个写入。设计要点：

- **最终一致**：即使某个后端写入失败，继续尝试其他后端。只有所有后端都失败时才返回错误。
- **最后错误**：返回最后一个失败的错误，而非第一个。在部分成功的场景下，最后一个错误更可能反映真实的问题（前面的错误可能是临时性的网络抖动）。
- **无事务保证**：写操作不是原子的——后端 A 成功但后端 B 失败时，数据存在不一致。这在全文搜索场景下是可接受的——搜索索引本身就是最终一致的辅助数据，丢失一条索引只影响搜索结果，不影响业务数据。

`InitIndex` 同样使用广播策略，确保所有 write 后端的索引同步创建。

**读操作——Failover 策略**

```go
func (se *SearchEngine) Search(q *SearchQuery) ([]string, error) {
    var lastErr error
    for _, b := range se.backends {
        if !b.HasRole("read") {
            continue
        }
        phids, err := b.Search(q)
        if err != nil {
            lastErr = err
            continue
        }
        return phids, nil
    }
    if lastErr != nil {
        return nil, fmt.Errorf("all fulltext search backends failed: %w", lastErr)
    }
    return nil, fmt.Errorf("no readable search backends configured")
}
```

读操作遍历所有拥有 `read` 角色的后端，首个成功即返回。设计要点：

- **快速返回**：不等待所有后端响应，第一个成功的结果就是最终结果。在正常情况下只有一个后端被调用。
- **自动 failover**：前一个后端失败时自动尝试下一个，对调用方完全透明。
- **错误分级**：区分"所有后端都失败"和"没有 read 后端"两种情况，返回不同的错误信息，便于排查问题。

`IndexExists`、`IndexStats`、`IndexIsSane` 同样使用 failover 策略——这些读操作只需从一个后端获取结果即可。

### 4.3 Elasticsearch 后端（internal/engine/elasticsearch）

Elasticsearch 后端是最复杂的模块，包含多主机管理、查询构建、索引配置三大功能。

#### 4.3.1 数据结构

```go
type Backend struct {
    hosts    []string
    index    string
    version  int
    timeout  int
    protocol string
    roles    map[string]bool

    mu     sync.RWMutex
    health map[string]bool
    client *http.Client
}
```

设计要点：

- **角色集合**：`roles` 使用 `map[string]bool` 实现 O(1) 的角色查询。默认分配 `read` + `write` 双角色。
- **健康状态**：`health` 记录每个主机的健康状态，初始全部为 `true`（健康）。`sync.RWMutex` 保护并发读写——搜索请求（读健康状态）远多于状态变更（写健康状态）。
- **共享 HTTP Client**：所有请求共享同一个 `http.Client`，利用其内置的连接池复用 TCP 连接。`Timeout` 统一设置，默认 15 秒。
- **索引名清理**：`strings.ReplaceAll(def.Index, "/", "")` 移除索引名中的斜杠，防止 URL 路径注入。

#### 4.3.2 多主机健康管理

```go
func (b *Backend) hostForRole(role string) (string, error) {
    if !b.roles[role] {
        return "", fmt.Errorf("backend does not have role %q", role)
    }
    b.mu.RLock()
    defer b.mu.RUnlock()

    healthy := make([]string, 0, len(b.hosts))
    for _, h := range b.hosts {
        if b.health[h] {
            healthy = append(healthy, h)
        }
    }
    if len(healthy) == 0 {
        return "", fmt.Errorf("no healthy hosts for role %q", role)
    }
    return healthy[rand.Intn(len(healthy))], nil
}
```

`hostForRole` 从健康主机中随机选取一个。设计要点：

**随机负载均衡**：使用 `rand.Intn` 随机选取，而非轮询或加权。随机选取在主机数量较少（2-5 个）时效果接近均匀分布，且无需维护状态（如轮询计数器）。

**读锁保护**：健康状态的查询使用读锁，不阻塞并发的搜索请求。写锁仅在 `markHealth` 时获取——健康状态变更的频率远低于查询。

**全部不健康时的处理**：当所有主机都不健康时返回错误，而非随机选取一个不健康的主机。这是一种快速失败（fail-fast）策略——避免向明确不可用的主机发送请求，加速错误返回。

```go
func (b *Backend) markHealth(host string, ok bool) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.health[host] = ok
}
```

健康标记在 HTTP 请求的响应处理中触发：

```go
func (b *Backend) doRequestRead(host, url, method string, body any) ([]byte, error) {
    // ...
    resp, err := b.client.Do(req)
    if err != nil {
        b.markHealth(host, false)
        return nil, fmt.Errorf("elasticsearch request failed: %w", err)
    }
    // ...
    if resp.StatusCode >= 500 {
        b.markHealth(host, false)
        return nil, fmt.Errorf(...)
    }
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf(...)
    }
    b.markHealth(host, true)
    return respBody, nil
}
```

健康标记的触发规则：

| 情况 | 标记 | 理由 |
|---|---|---|
| 网络错误（连接失败、超时） | 不健康 | 主机可能宕机或网络不可达 |
| 5xx 响应 | 不健康 | 主机内部错误，可能过载或崩溃 |
| 4xx 响应 | 不标记 | 客户端错误（如索引不存在），主机本身正常 |
| 2xx 响应 | 恢复健康 | 主机已恢复正常 |
| 读取响应体失败 | 不健康 | 连接可能中断 |

4xx 不标记不健康的设计尤为重要——`IndexExists` 在索引不存在时收到 404 是正常行为，不应影响主机的健康状态。

#### 4.3.3 查询构建

查询构建器将 Phorge 的 `SearchQuery` 参数翻译为 Elasticsearch Bool Query DSL。

**全文搜索**

```go
if q.Query != "" {
    bq.AddMust(map[string]any{
        "simple_query_string": map[string]any{
            "query":  q.Query,
            "fields": []string{
                esquery.FieldTitle + ".*",
                esquery.FieldBody + ".*",
                esquery.FieldComment + ".*",
            },
            "default_operator": "AND",
        },
    })

    bq.AddShould(map[string]any{
        "simple_query_string": map[string]any{
            "query":  q.Query,
            "fields": []string{
                "*.raw",
                esquery.FieldTitle + "^4",
                esquery.FieldBody + "^3",
                esquery.FieldComment + "^1.2",
            },
            "analyzer":         "english_exact",
            "default_operator": "and",
        },
    })
}
```

全文搜索使用两层查询策略：

**Must 层**：使用 `simple_query_string` 在标题、正文、评论的所有子字段（`.raw`、`.keywords`、`.stems`）上搜索，默认 `AND` 运算符要求所有关键词都匹配。通配符 `.*` 让搜索同时覆盖精确匹配、关键词和词干三个子字段，提高召回率。

**Should 层**：额外的精确匹配加分查询。使用 `english_exact` 分析器（只做 lowercase，不做词干提取），对标题（权重 ×4）、正文（×3）、评论（×1.2）的精确匹配给予额外分数。这确保了精确匹配的文档排名高于词干匹配的文档——搜索 "login" 时，标题包含 "login" 的文档排名高于标题包含 "logging" 的文档。

**关系过滤**

```go
relMap := map[string][]string{
    esquery.RelAuthor:     q.AuthorPHIDs,
    esquery.RelSubscriber: q.SubscriberPHIDs,
    esquery.RelProject:    q.ProjectPHIDs,
    esquery.RelRepository: q.RepositoryPHIDs,
}
for field, phids := range relMap {
    if len(phids) > 0 {
        bq.AddTerms(field, phids)
    }
}
```

关系过滤通过 `terms` 查询实现——精确匹配 PHID 数组中的任意一个。`AddTerms` 将条件添加到 Bool Query 的 `filter` 子句中（不影响评分，只过滤）。

**状态过滤**

```go
statusSet := make(map[string]bool, len(q.Statuses))
for _, s := range q.Statuses {
    statusSet[s] = true
}
includeOpen := statusSet[esquery.RelOpen]
includeClosed := statusSet[esquery.RelClosed]
if includeOpen && !includeClosed {
    bq.AddExists(esquery.RelOpen)
} else if !includeOpen && includeClosed {
    bq.AddExists(esquery.RelClosed)
}
```

状态过滤使用 `exists` 查询——Phorge 的状态模型不是存储显式的状态值，而是通过"字段是否存在"来表示。文档有 `open` 关系字段表示处于打开状态，有 `closed` 关系字段表示已关闭。当同时包含 open 和 closed 时不添加过滤——等价于搜索所有状态。

**空查询处理**

```go
if bq.MustCount() == 0 {
    bq.AddMust(map[string]any{
        "match_all": map[string]any{"boost": 1},
    })
}

if q.Query == "" {
    spec["sort"] = []any{
        map[string]string{"dateCreated": "desc"},
    }
}
```

当没有关键词搜索时，添加 `match_all` 查询匹配所有文档，并按 `dateCreated` 降序排序。这对应 Phorge 的"浏览"模式——不搜索关键词，而是按时间倒序列出文档。

**分页安全**

```go
offset := q.Offset
limit := q.Limit
if limit == 0 {
    limit = 101
}
if offset+limit > 10000 {
    offset = 10000 - limit
    if offset < 0 {
        offset = 0
    }
}
```

分页参数有两重保护：默认 limit 为 101（Phorge 默认每页 100 条，多取一条用于判断是否有下一页）；`offset + limit` 上限为 10000，防止 Elasticsearch 的 `max_result_window` 限制报错。超过上限时自动调整 offset，而非返回错误——静默降级比报错中断更适合搜索场景。

#### 4.3.4 索引配置

`buildIndexConfig` 构建完整的 Elasticsearch 索引配置，包含自定义分析器和字段映射。

**分析器体系**

```go
"analyzer": map[string]any{
    "english_exact": map[string]any{
        "tokenizer": "standard",
        "filter":    []string{"lowercase"},
    },
    "letter_stop": map[string]any{
        "tokenizer": "letter",
        "filter":    []string{"lowercase", "english_stop"},
    },
    "english_stem": map[string]any{
        "tokenizer": "standard",
        "filter": []string{
            "english_possessive_stemmer",
            "lowercase",
            "english_stop",
            "english_stemmer",
        },
    },
},
```

三个分析器构成从精确到模糊的搜索梯度：

| 分析器 | 处理链 | 用途 |
|---|---|---|
| `english_exact` | standard 分词 → lowercase | 精确匹配，仅大小写归一化 |
| `letter_stop` | letter 分词 → lowercase → 停用词过滤 | 关键词匹配，过滤 the/a/is 等无意义词 |
| `english_stem` | standard 分词 → 所有格去除 → lowercase → 停用词 → 词干提取 | 模糊匹配，running/runs/ran → run |

**字段映射**

每个文档字段（标题、正文、评论等）同时建立三个子字段：

```go
props[f] = map[string]any{
    "type": textType,
    "fields": map[string]any{
        "raw":      { "analyzer": "english_exact", ... },
        "keywords": { "analyzer": "letter_stop" },
        "stems":    { "analyzer": "english_stem" },
    },
}
```

这种多子字段设计让同一个搜索词同时在三个分析维度上匹配，结合查询构建中的 `should` 加分机制，实现了"精确匹配排名高、模糊匹配也能召回"的搜索体验。

**版本兼容**

```go
func (b *Backend) textFieldType() string {
    if b.version >= 5 {
        return "text"
    }
    return "string"
}
```

根据 Elasticsearch 版本自动选择正确的字段类型名。ES 5 将原来的 `string` 类型拆分为 `text`（全文搜索）和 `keyword`（精确匹配），旧版本仍使用 `string`。关系字段在 ES 5+ 使用 `keyword` + `doc_values: false`（不需要聚合），旧版本使用 `string` + `index: not_analyzed`。

#### 4.3.5 索引健康检查

```go
func (b *Backend) IndexIsSane(docTypes []string) (bool, error) {
    exists, err := b.IndexExists()
    if err != nil || !exists {
        return false, err
    }
    // 获取当前 mapping 和 settings
    // ...
    actual := mergeAny(
        asMap(settingsResp[b.index]),
        asMap(mappingResp[b.index]),
    )
    expected := b.buildIndexConfig(docTypes)
    return configDeepMatch(actual, expected), nil
}
```

`IndexIsSane` 通过三步验证索引健康：

1. **存在性检查**：先确认索引存在，不存在直接返回 `false`。
2. **配置获取**：分别获取当前索引的 `_mapping` 和 `_settings`，合并为完整的配置快照。
3. **深度比对**：将实际配置与期望配置进行递归比对。

```go
func configDeepMatch(actual, required map[string]any) bool {
    for key, rval := range required {
        aval, exists := actual[key]
        if !exists {
            if key == "_all" {
                continue
            }
            return false
        }
        rmap, rIsMap := rval.(map[string]any)
        if rIsMap {
            amap, aIsMap := aval.(map[string]any)
            if !aIsMap {
                return false
            }
            if !configDeepMatch(amap, rmap) {
                return false
            }
            continue
        }
        if normalizeConfigValue(aval) != normalizeConfigValue(rval) {
            return false
        }
    }
    return true
}
```

`configDeepMatch` 是单向的子集匹配——只检查期望配置中的每个字段是否在实际配置中存在且值一致，不要求实际配置完全等于期望配置。这是因为 Elasticsearch 在创建索引后会添加大量默认设置（如 `number_of_shards`、`codec` 等），这些额外字段不应被视为"不一致"。

`_all` 字段被特殊处理——ES 6+ 已移除 `_all` 字段，但旧版本的索引配置中可能包含。跳过 `_all` 的缺失检查确保了跨版本兼容。

`normalizeConfigValue` 将 `bool`、`float64`（JSON 数字的默认类型）、`string` 统一为字符串进行比对，处理 Elasticsearch 在响应中将 `true` 返回为 `"true"` 等类型不一致的情况。

### 4.4 Meilisearch 后端（internal/engine/meilisearch）

Meilisearch 后端将 Phorge/Elasticsearch 的概念映射到 Meilisearch 的数据模型。

#### 4.4.1 概念映射

```go
// Key mapping from Phorge/ES concepts:
//   - ES index  -> Meilisearch index (uid)
//   - ES type   -> stored as a filterable "docType" attribute
//   - ES _id    -> Meilisearch document primary key "id" (= PHID)
//   - ES fields -> flattened into top-level Meilisearch attributes
```

| Phorge/ES 概念 | Meilisearch 映射 | 差异说明 |
|---|---|---|
| ES index | index (uid) | 一对一映射，默认 `phabricator` |
| ES type (doc type) | `docType` 可过滤属性 | ES 用 URL 路径区分类型，Meilisearch 用属性过滤 |
| ES `_id` | `id` 主键 | 直接使用 PHID 作为主键 |
| ES 文本字段 + 子字段 | 顶层属性 | Meilisearch 不支持子字段，直接存为顶层属性 |
| ES Bool Query | filter 表达式 | Meilisearch 使用类 SQL 的过滤语法 |
| ES `_source: false` | `attributesToRetrieve: ["id"]` | 都只返回标识符，不返回文档内容 |

#### 4.4.2 文档构建

```go
func (b *Backend) buildDocument(doc *engine.Document) map[string]any {
    m := map[string]any{
        "id":           doc.PHID,
        "docType":      doc.Type,
        "title":        doc.Title,
        "dateCreated":  doc.DateCreated,
        "lastModified": doc.DateModified,
    }

    for _, f := range doc.Fields {
        key := f.Name
        existing, ok := m[key]
        if ok {
            // 多值合并为数组
        } else {
            if f.Aux != "" {
                m[key] = f.Corpus + " " + f.Aux
            } else {
                m[key] = f.Corpus
            }
        }
    }
    // ...
}
```

与 Elasticsearch 后端的 `buildDocSpec` 相比，Meilisearch 的文档构建有一个关键差异：单值字段将 `Corpus` 和 `Aux` 合并为空格拼接的字符串（`f.Corpus + " " + f.Aux`），而非存为数组。这是因为 Meilisearch 对字符串和数组的搜索行为不同——单个字符串的全文搜索效果优于对数组元素逐个搜索。

#### 4.4.3 搜索请求

```go
func (b *Backend) buildSearchRequest(q *engine.SearchQuery) map[string]any {
    req := map[string]any{
        "q":                    q.Query,
        "attributesToRetrieve": []string{"id"},
    }
    // ...
    if q.Query == "" {
        req["sort"] = []string{"dateCreated:desc"}
    }
    return req
}
```

Meilisearch 的搜索 API 比 Elasticsearch 简洁：全文搜索直接传递 `q` 参数，Meilisearch 内部处理分词、相关性排序和拼写纠正。`attributesToRetrieve: ["id"]` 限制返回字段，减少网络传输——搜索结果只需要 PHID，Phorge 会用 PHID 从数据库获取完整数据。

#### 4.4.4 过滤语法

```go
func (b *Backend) buildFilters(q *engine.SearchQuery) []any {
    var filters []any

    if len(q.Types) > 0 {
        typeFilters := make([]string, 0, len(q.Types))
        for _, t := range q.Types {
            typeFilters = append(typeFilters, fmt.Sprintf("docType = %s", t))
        }
        if len(typeFilters) == 1 {
            filters = append(filters, typeFilters[0])
        } else {
            filters = append(filters, typeFilters)
        }
    }
    // ...
}
```

Meilisearch 的过滤使用类 SQL 语法（`docType = TASK`），而非 Elasticsearch 的 JSON DSL。多类型过滤时使用数组嵌套实现 OR 逻辑——Meilisearch 的过滤规则中，数组内的条件是 OR 关系，数组间的条件是 AND 关系。

状态过滤使用 `EXISTS` 关键字（`open EXISTS`），对应 Elasticsearch 的 `exists` 查询。

#### 4.4.5 索引初始化

```go
func (b *Backend) InitIndex(docTypes []string) error {
    _ = b.deleteIndex()

    createBody, _ := json.Marshal(map[string]string{
        "uid":        b.index,
        "primaryKey": "id",
    })
    url := fmt.Sprintf("%s/indexes", b.host)
    _, err := b.doRequest(url, http.MethodPost, bytes.NewReader(createBody))
    if err != nil {
        return fmt.Errorf("create index: %w", err)
    }

    if err := b.waitForIdle(); err != nil {
        return err
    }

    return b.configureIndex()
}
```

初始化流程分三步：

1. **删除旧索引**：忽略删除错误（索引可能不存在）。
2. **创建新索引**：指定 `id` 为主键（= PHID）。
3. **等待并配置**：Meilisearch 的索引创建是异步的，需要轮询等待完成后再配置设置。

```go
func (b *Backend) waitForIdle() error {
    for i := 0; i < 30; i++ {
        time.Sleep(200 * time.Millisecond)
        url := fmt.Sprintf("%s/tasks?statuses=enqueued,processing&limit=1", b.host)
        body, err := b.doRequest(url, http.MethodGet, nil)
        if err != nil {
            continue
        }
        var resp struct {
            Total int `json:"total"`
        }
        if json.Unmarshal(body, &resp) == nil && resp.Total == 0 {
            return nil
        }
    }
    return nil
}
```

`waitForIdle` 轮询 Meilisearch 的任务队列，每 200ms 检查一次是否还有排队或处理中的任务。最多等待 6 秒（30 × 200ms），超时后静默返回——不报错是因为 `configureIndex` 的请求也会被 Meilisearch 排队处理，不一定需要前一个任务完成。

```go
func (b *Backend) configureIndex() error {
    settings := msSettings{
        SearchableAttributes: b.searchableAttributes(),
        FilterableAttributes: b.filterableAttributes(),
        SortableAttributes:   b.sortableAttributes(),
        DisplayedAttributes:  []string{"id", "docType"},
        RankingRules:         []string{
            "words", "typo", "proximity", "attribute", "sort", "exactness",
        },
    }
    // ...
}
```

索引配置定义了：

- **可搜索属性**：`title` + 所有 Phorge 字段常量（`titl`、`body`、`cmnt`、`full`、`core`）
- **可过滤属性**：`docType` + 所有关系常量（`auth`、`ownr`、`proj` 等）
- **可排序属性**：`dateCreated`、`lastModified`
- **展示属性**：仅 `id` 和 `docType`，最小化返回数据
- **排名规则**：Meilisearch 默认的 6 条排名规则，依次按词频、拼写纠正、邻近度、属性权重、排序、精确度排名

#### 4.4.6 健康检查

```go
func (b *Backend) IndexIsSane(_ []string) (bool, error) {
    // ...
    var settings msSettings
    // ...
    expectedSearchable := b.searchableAttributes()
    if len(settings.SearchableAttributes) != len(expectedSearchable) {
        return false, nil
    }

    expectedFilterable := b.filterableAttributes()
    filterSet := make(map[string]bool, len(settings.FilterableAttributes))
    for _, f := range settings.FilterableAttributes {
        filterSet[f] = true
    }
    for _, f := range expectedFilterable {
        if !filterSet[f] {
            return false, nil
        }
    }
    return true, nil
}
```

Meilisearch 的健康检查比 Elasticsearch 简单——只验证可搜索属性的数量和可过滤属性的集合匹配。`docTypes` 参数被忽略（`_ []string`），因为 Meilisearch 不按文档类型创建不同的映射。

### 4.5 ES Query 构建器（internal/esquery）

#### 4.5.1 Bool Query 抽象

```go
type BoolQuery struct {
    Must    []any `json:"must,omitempty"`
    Should  []any `json:"should,omitempty"`
    Filter  []any `json:"filter,omitempty"`
    MustNot []any `json:"must_not,omitempty"`
}
```

`BoolQuery` 是 Elasticsearch Bool Query 的直接映射，支持四种子句：

| 子句 | 语义 | 是否影响评分 |
|---|---|---|
| `Must` | 文档必须匹配 | 是 |
| `Should` | 匹配时加分 | 是 |
| `Filter` | 文档必须匹配 | 否 |
| `MustNot` | 文档不能匹配 | 否 |

使用 `omitempty` 确保空数组不会出现在 JSON 输出中——Elasticsearch 对某些版本中空的 `must` 或 `filter` 数组可能报错。

#### 4.5.2 Phorge 常量

```go
const (
    FieldTitle   = "titl"
    FieldBody    = "body"
    FieldComment = "cmnt"
    FieldAll     = "full"
    FieldCore    = "core"
)

const (
    RelAuthor     = "auth"
    RelOwner      = "ownr"
    RelProject    = "proj"
    RelRepository = "repo"
    RelOpen       = "open"
    RelClosed     = "clos"
    RelUnowned    = "unow"
    // ...
)
```

这些常量直接来自 Phorge PHP 端的 `PhabricatorSearchDocumentFieldType` 和 `PhabricatorSearchRelationship` 类。使用 4 字符缩写（`titl`、`auth`、`ownr`）是 Phorge 的历史设计——Phabricator 早期使用 MySQL 的 InnoDB 全文索引，列名长度受限。gorge-search 完全保持这些缩写不变，确保索引数据与 Phorge PHP 端兼容。

`AllFields()` 和 `AllRelationships()` 函数返回所有常量的切片，用于索引配置时为每个文档类型创建完整的字段映射。

### 4.6 HTTP API 层（internal/httpapi）

#### 4.6.1 路由注册

```go
func RegisterRoutes(e *echo.Echo, deps *Deps) {
    e.GET("/", healthPing())
    e.GET("/healthz", healthPing())

    g := e.Group("/api/search")
    g.Use(tokenAuth(deps))

    g.POST("/index", indexDocument(deps))
    g.POST("/query", searchQuery(deps))
    g.POST("/init", initIndex(deps))
    g.GET("/exists", indexExists(deps))
    g.GET("/stats", indexStats(deps))
    g.POST("/sane", indexIsSane(deps))
    g.GET("/backends", listBackends(deps))
}
```

路由分为两组：健康检查端点（`/` 和 `/healthz`）不需要认证，直接挂在根路由上；搜索 API 端点（`/api/search/*`）通过路由组统一应用 `tokenAuth` 中间件。

`Deps` 结构体作为依赖注入容器，持有 `SearchEngine` 和认证 Token，传递给所有 handler 闭包。

#### 4.6.2 认证中间件

```go
func tokenAuth(deps *Deps) echo.MiddlewareFunc {
    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return func(c echo.Context) error {
            if deps.Token == "" {
                return next(c)
            }
            token := c.Request().Header.Get("X-Service-Token")
            if token == "" {
                token = c.QueryParam("token")
            }
            if token == "" || token != deps.Token {
                return c.JSON(http.StatusUnauthorized, &apiResponse{
                    Error: &apiError{Code: "ERR_UNAUTHORIZED", Message: "missing or invalid service token"},
                })
            }
            return next(c)
        }
    }
}
```

认证设计要点：

- **可选认证**：`Token` 为空时跳过认证，适合开发环境。生产环境应始终设置 Token。
- **双通道传递**：Token 可通过 `X-Service-Token` 请求头或 `?token=` 查询参数传递。请求头优先。查询参数方式便于快速调试（如浏览器直接访问），但生产环境应使用请求头（查询参数可能出现在日志中）。
- **常量时间比较**：当前使用 `!=` 直接比较。在内部服务间通信的场景下，时序攻击的风险极低——Token 不通过公网传输，且攻击者需要先突破网络隔离。

#### 4.6.3 统一响应格式

```go
type apiResponse struct {
    Data  any       `json:"data,omitempty"`
    Error *apiError `json:"error,omitempty"`
}

type apiError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}
```

所有 API 响应遵循统一的信封格式：成功时 `data` 字段包含结果，`error` 为空；失败时 `error` 字段包含错误码和消息，`data` 为空。`omitempty` 确保空字段不出现在 JSON 中——成功响应不包含 `error` 字段，错误响应不包含 `data` 字段。

错误码以 `ERR_` 前缀命名，涵盖认证、参数、索引、搜索、统计等各类错误场景。502 状态码用于所有后端通信失败的情况——表示 gorge-search 本身正常，但上游搜索引擎不可用。

### 4.7 入口程序

```go
func main() {
    var cfg *config.Config

    if path := os.Getenv("SEARCH_CONFIG_FILE"); path != "" {
        cfg, err = config.LoadFromFile(path)
    } else {
        cfg = config.LoadFromEnv()
    }

    backends := buildBackends(cfg.Backends)
    se := engine.New(backends)

    e := echo.New()
    e.Use(middleware.RequestLoggerWithConfig(...))
    e.Use(middleware.Recover())

    httpapi.RegisterRoutes(e, &httpapi.Deps{
        Engine: se,
        Token:  cfg.ServiceToken,
    })

    e.Logger.Fatal(e.Start(cfg.ListenAddr))
}

func buildBackends(defs []config.BackendDef) []engine.SearchBackend {
    var backends []engine.SearchBackend
    for _, d := range defs {
        switch d.Type {
        case "meilisearch":
            backends = append(backends, meilisearch.New(d))
        default:
            backends = append(backends, elasticsearch.New(d))
        }
    }
    return backends
}
```

启动流程的设计要点：

- **配置优先级**：`SEARCH_CONFIG_FILE` 存在时使用 JSON 文件，否则使用环境变量。
- **后端工厂**：`buildBackends` 根据 `Type` 字段选择具体的后端实现。`default` 分支走 Elasticsearch——确保未指定类型或拼写错误时不会静默失败，而是使用最常用的引擎。
- **无后端警告**：未配置任何后端时打印警告日志但不退出——服务仍可启动，可以通过 `/healthz` 检查存活性，待配置后端后重启。
- **Echo 中间件**：`RequestLogger` 记录每个请求的方法、路径和状态码；`Recover` 捕获 handler 中的 panic，防止单个请求的异常导致进程崩溃。

## 5. 并发模型

### 5.1 锁设计

gorge-search 的并发控制集中在 Elasticsearch 后端的健康管理：

```
Backend.mu (RWMutex)
  └── 保护 health map（每主机健康状态）
      │
      ├── hostForRole()    使用读锁，搜索请求的热路径
      ├── allHostsForRole()  无锁，返回完整主机列表
      └── markHealth()     使用写锁，请求完成后标记
```

锁的使用遵循"读多写少"原则：

- **读锁**：`hostForRole` 在每次搜索请求时调用，使用读锁快速查询健康主机。多个搜索请求可以并行执行，不会互相阻塞。
- **写锁**：`markHealth` 在请求完成后调用（成功或失败），使用写锁更新健康状态。写锁的持有时间极短——仅一次 map 赋值。

`allHostsForRole` 不使用锁，因为它返回的是初始化后不变的 `hosts` 切片，不存在并发修改的风险。`health` map 的并发访问需要锁保护，但 `hosts` 切片创建后只读，Go 的内存模型保证了并发读的安全性。

### 5.2 HTTP Client 并发

```go
b.client = &http.Client{Timeout: time.Duration(b.timeout) * time.Second}
```

每个后端实例共享一个 `http.Client`。`http.Client` 的 `Transport` 内置连接池（默认 `MaxIdleConns: 100`、`MaxIdleConnsPerHost: 2`），自动管理到搜索引擎的 TCP 连接复用。`http.Client` 本身是并发安全的，多个 goroutine 可以同时使用同一个 Client 发起请求。

`Timeout` 设置在 Client 级别而非请求级别，覆盖了连接建立、TLS 握手、请求发送、响应读取的完整生命周期。默认 15 秒足够覆盖正常的搜索引擎响应时间，同时在引擎不可用时不会阻塞过长。

## 6. 与 Phorge PHP 实现的对应关系

| Go 实现 | PHP 类 | 差异说明 |
|---|---|---|
| `engine.SearchBackend` | `PhabricatorFulltextStorageEngine` | 接口抽象，方法签名对齐 |
| `engine.Document` | `PhabricatorSearchAbstractDocument` | 字段和关系的数据结构 |
| `engine.SearchQuery` | `PhabricatorSavedQuery` | 查询参数集合 |
| `elasticsearch.Backend` | `PhabricatorElasticFulltextStorageEngine` | ES 交互逻辑 |
| `esquery.BoolQuery` | `PhabricatorElasticsearchQueryBuilder` | Bool Query DSL 构建 |
| `esquery.Field*` / `Rel*` 常量 | `PhabricatorSearchDocumentFieldType` / `PhabricatorSearchRelationship` | 字段和关系标识符 |
| `buildIndexConfig()` | `PhabricatorElasticSearchSetup` | 索引映射和分析器配置 |
| `buildSearchSpec()` | `executeSearch()` | 搜索 DSL 构建逻辑 |
| `buildDocSpec()` | `indexDocument()` | 文档索引格式 |

关键行为兼容点：

- **字段缩写**：`titl`、`body`、`cmnt`、`auth`、`ownr` 等 4 字符缩写与 PHP 端完全一致。
- **默认索引名**：`phabricator`，与 PHP 端配置的默认值一致。
- **分页默认值**：limit 默认 101（PHP 端默认 100 + 1 用于判断下一页）。
- **搜索排序**：有关键词时按相关性排序，无关键词时按 `dateCreated` 降序。
- **分析器配置**：`english_exact`、`letter_stop`、`english_stem` 三个分析器与 PHP 端构建的索引配置一致。
- **关系过滤**：状态过滤使用 `exists` 查询，与 PHP 端的逻辑一致。

## 7. 部署方案

### 7.1 Docker 镜像

采用多阶段构建：

- **构建阶段**：基于 `golang:1.26-alpine3.22`，使用 `CGO_ENABLED=0` 静态编译，`-ldflags="-s -w"` 去除调试信息和符号表以缩小二进制体积。
- **运行阶段**：基于 `alpine:3.20`，仅包含编译后的二进制和 CA 证书（搜索引擎可能使用 HTTPS）。

暴露 8120 端口。

内置 Docker `HEALTHCHECK`，每 10 秒通过 `wget` 检查 `/healthz` 端点，启动等待 5 秒，超时 3 秒，最多重试 3 次。

### 7.2 部署建议

**网络隔离**：gorge-search 应部署在与 Phorge PHP 应用相同的内部网络中，不直接暴露给外部用户。通过 `SERVICE_TOKEN` 进行服务间认证。

**搜索引擎连接**：gorge-search 到 Elasticsearch/Meilisearch 的连接通常使用 HTTP（内部网络）。如需 HTTPS，通过 `ES_PROTOCOL=https` 或配置文件中的 `protocol` 字段指定。

**多后端部署**：通过 JSON 配置文件或 `SEARCH_BACKENDS` 环境变量配置多个后端，实现读写分离或引擎迁移。例如，将 Elasticsearch 设为 `read` + `write`，Meilisearch 设为 `write` only，用于并行写入验证。

**资源估算**：gorge-search 是无状态的代理服务，不存储数据，内存占用主要来自 Go 运行时和 HTTP 连接池。每个到搜索引擎的连接约占 4-8KB，典型部署内存在 20-30MB 级别。CPU 消耗主要在 JSON 编解码和 HTTP 请求处理，通常不是瓶颈。

## 8. 依赖分析

| 依赖 | 版本 | 用途 |
|---|---|---|
| `labstack/echo` | v4.15.1 | HTTP 框架，路由注册、中间件、请求绑定 |

直接依赖仅一个。搜索引擎交互使用标准库 `net/http`，JSON 编解码使用标准库 `encoding/json`，并发控制使用标准库 `sync`。

选择 Echo 而非标准库 `net/http` 的原因：Echo 提供了路由组、中间件链、请求绑定（`c.Bind`）、JSON 响应（`c.JSON`）等便利功能，减少了样板代码。与 Gorge 平台的其他微服务（gorge-db-api 等）保持技术栈一致。

不依赖任何 Elasticsearch 或 Meilisearch 的官方 Go SDK——所有交互通过标准库 `net/http` 直接调用 REST API。这减少了依赖链，避免了 SDK 版本与搜索引擎版本的兼容性问题，也使得代码对搜索引擎 API 的调用完全透明可控。

## 9. 测试覆盖

项目包含三组测试文件，覆盖核心模块：

| 测试文件 | 覆盖范围 |
|---|---|
| `config_test.go` | 环境变量默认值验证、ES 环境变量解析（多主机/索引名/版本号）、JSON 环境变量解析、Meilisearch 环境变量解析（含 API Key）、Meilisearch 无主机时的空后端 |
| `engine_test.go` | 引擎创建（有/无后端）、文档索引写入、搜索查询、无可读后端错误、索引初始化、索引存在性检查、后端信息查询、多后端 failover（failing + working 后端组合） |
| `handlers_test.go` | 健康检查端点、Token 认证（无 Token/有效 Token/查询参数/认证关闭）、文档索引参数校验（缺少 PHID）、搜索查询无后端错误（502） |

测试设计的特点：

- **Mock 后端**：`engine_test.go` 定义了 `mockBackend`（正常后端，内存 map 存储）和 `failingBackend`（始终失败），用于测试调度逻辑和 failover 行为，不依赖真实的搜索引擎实例。
- **端到端测试**：`handlers_test.go` 使用 `httptest.NewRecorder` 构建完整的 HTTP 请求-响应链路，验证从请求解析到 JSON 响应的完整流程。
- **认证矩阵**：认证测试覆盖了四种场景——无 Token 被拒、有效 Token 通过、查询参数 Token 通过、认证关闭时无需 Token。

## 10. 总结

gorge-search 是一个职责单一的全文搜索代理微服务，核心价值在于：

1. **多引擎抽象**：通过 `SearchBackend` 接口统一 Elasticsearch 和 Meilisearch 的操作，新增搜索引擎只需实现接口，无需修改上层代码。PHP 端通过统一的 HTTP API 调用，完全不感知底层引擎差异。
2. **读写分离与故障转移**：写操作广播到所有 write 后端确保数据冗余，读操作自动 failover 到下一个 read 后端确保服务可用性。多主机随机选取实现简单的负载均衡。
3. **健康管理**：Elasticsearch 后端自动跟踪每个主机的健康状态，5xx 和网络错误标记不健康，成功请求恢复健康。不健康的主机被跳过，减少请求延迟和错误率。
4. **Phorge 深度兼容**：字段缩写、关系常量、分析器配置、查询构建逻辑与 Phorge PHP 端完全对齐，确保索引数据的二进制兼容——可以在不重建索引的情况下从 PHP 直连切换到 gorge-search 代理。
5. **零 SDK 依赖**：不依赖任何搜索引擎的官方 SDK，唯一的第三方依赖是 Echo HTTP 框架。搜索引擎交互全部使用标准库 `net/http`，代码透明可控，易于理解和维护。
