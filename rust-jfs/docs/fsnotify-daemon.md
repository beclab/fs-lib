# fsnotify-daemon 流程文档

## 概述

`fsnotify-daemon` 是一个运行在 Kubernetes 集群中的 DaemonSet 服务，用于监控文件系统变化并将事件发布到 Redis。它是文件系统监控系统的服务端组件，通过监听 FSWatcher CRD 资源来动态管理监控路径。

## 架构设计

### 核心组件

1. **Main Entry**: 程序入口，初始化所有组件
2. **App (v2)**: 应用主逻辑，管理文件系统监控和事件发布
3. **Controller**: Kubernetes Controller，监听 FSWatcher CRD 变化
4. **Informer**: Kubernetes Informer，缓存和同步 FSWatcher 资源
5. **Producer**: Redis 客户端，用于发布事件
6. **FSWatcher**: 本地文件系统监控器（基于 fsnotify）

### 版本说明

项目包含两个版本的实现：
- **v1 (app/app.go)**: 使用 IPC 与 JuiceFS 通信
- **v2 (app/v2/app.go)**: 使用本地 fsnotify 监控（当前使用）

本文档主要描述 v2 版本的实现。

## 启动流程

### 1. 初始化阶段 (`main()`)

```go
func main() {
    // 1. 初始化日志
    klog.InitFlags(nil)
    pflag.Parse()
    
    // 2. 获取 Kubernetes 配置
    config := ctrl.GetConfigOrDie()
    
    // 3. 设置信号处理
    apiCtx, cancel := context.WithCancel(context.Background())
    stopCh := signals.SetupSignalHandler(apiCtx, cancel)
    
    // 4. 创建 Kubernetes 客户端
    sysClient := sysclientset.NewForConfigOrDie(config)
    
    // 5. 创建 Informer Factory
    informerFactory := informers.NewSharedInformerFactory(sysClient, 0)
    informer := informerFactory.Sys().V1alpha1().FSWatchers()
    
    // 6. 创建 App 实例
    app, err := appv2.New(apiCtx, informer.Lister(), stopCh)
    
    // 7. 创建 Controller
    controller := fswatchers.NewController(sysClient, informer, app.SyncWatches)
    
    // 8. 启动 Informer 和 Controller
    informerFactory.Start(stopCh)
    controller.Run(1, stopCh)
}
```

**关键步骤：**

1. **日志初始化**: 使用 klog 进行日志记录
2. **Kubernetes 配置**: 通过 controller-runtime 获取集群配置
3. **信号处理**: 监听 SIGTERM 和 SIGINT 信号，优雅关闭
4. **客户端创建**: 创建自定义资源客户端
5. **Informer 创建**: 创建 FSWatcher 资源的 Informer
6. **App 初始化**: 创建应用实例，初始化 Redis 和文件系统监控
7. **Controller 创建**: 创建 Controller 并注册事件处理器

### 2. App 初始化 (`app/v2.New()`)

```go
func New(ctx context.Context, lister listers.FSWatcherLister, stopCh <-chan struct{}) (*App, error)
```

**初始化内容：**

1. **Redis 客户端创建**
   - 连接 Redis 服务器（默认: `redis.kubesphere-system:6379`）
   - 使用 DB 10
   - 支持环境变量配置：
     - `REDIS_ADDR`: Redis 地址
     - `REDIS_PASSWORD`: Redis 密码

2. **文件系统监控器创建**
   - 在独立 goroutine 中创建 `fsnotify.Watcher`
   - 启动事件循环 (`run()`)
   - 支持自动重启（监控器异常退出时）

3. **Watcher Lister 保存**
   - 保存 FSWatcher Lister 用于查询资源

## 核心流程

### 1. 文件系统事件监控 (`run()`)

**事件循环流程：**

```go
func (a *App) run(stopCh <-chan struct{}) (bool, error) {
    for {
        select {
        case e, ok := <-a.watchers.Events:
            // 处理文件系统事件
        case err, ok := <-a.watchers.Errors:
            // 处理错误
        case <-stopCh:
            // 优雅关闭
        }
    }
}
```

**事件处理步骤：**

1. **接收事件**: 从 `fsnotify.Watcher` 的 Events 通道接收事件
2. **事件转换**:
   - 解析文件路径，提取 Key（父目录路径）
   - 转换为 `jfsnotify.Event` 格式
   ```go
   event := jfsnotify.Event{
       Name: e.Name,  // 完整文件路径
       Op:   jfsnotify.Op(e.Op),  // 操作类型
       Key:  key,  // 父目录路径
   }
   ```
3. **序列化**: 将事件数组序列化为 JSON
4. **发布到 Redis**: 通过 Redis Pub/Sub 发布事件
   - Channel: `"jfsnotify"`
   - Message: JSON 格式的事件数组

**错误处理：**

- 监控器错误会记录日志
- 序列化错误会跳过当前事件
- 发布错误会记录日志但不中断流程

### 2. Controller 工作流程

#### 2.1 事件监听

Controller 通过 Informer 监听 FSWatcher CRD 的变化：

```go
prInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
    AddFunc:    controller.handleAddObject,    // 资源创建
    UpdateFunc: controller.handleUpdateObject, // 资源更新
    DeleteFunc: controller.handleDeleteObject, // 资源删除
})
```

#### 2.2 事件入队

**处理流程：**

1. **Add 事件**: 资源创建时入队
2. **Update 事件**: 
   - 比较新旧对象（通过 JSON 序列化比较）
   - 仅在对象真正变化时入队
3. **Delete 事件**: 资源删除时入队

**入队对象格式：**
```go
type enqueueObj struct {
    action Action  // ADD, UPDATE, DELETE
    obj    interface{}  // FSWatcher 对象
}
```

#### 2.3 工作队列处理

**Worker 流程：**

1. **获取任务**: 从工作队列获取任务
2. **处理任务**: 调用 `syncHandler` 处理
3. **错误处理**:
   - 成功: 从队列中移除（Forget）
   - 失败: 重新入队（RateLimited），支持重试

**处理函数：**
```go
func (c *Controller) syncHandler(obj enqueueObj) error {
    return c.handler(obj.action, obj.obj)
}
```

### 3. 监控路径同步 (`SyncWatches()`)

**触发时机：**

- FSWatcher 资源创建 (ADD)
- FSWatcher 资源更新 (UPDATE)
- FSWatcher 资源删除 (DELETE)

**同步逻辑：**

```go
func (a *App) SyncWatches(action fswatchers.Action, obj interface{}) error {
    // 1. 获取所有 FSWatcher 资源
    list, err := a.watcherLister.List(labels.Everything())
    
    // 2. 遍历所有资源，添加监控路径
    for _, l := range list {
        for _, p := range l.Spec.Paths {
            a.watchers.Add(p)  // fsnotify 会自动去重
        }
    }
    
    return nil
}
```

**特点：**

- **全量同步**: 每次变更都会重新同步所有路径
- **自动去重**: `fsnotify.Watcher.Add()` 会自动忽略重复路径
- **简单高效**: 不需要复杂的增量计算

## FSWatcher CRD 资源

### 资源定义

```yaml
apiVersion: sys.bytetrade.io/v1alpha1
kind: FSWatcher
metadata:
  name: <watcher-name>
  namespace: <namespace>
spec:
  description: "描述信息"
  pod: "pod-name"
  container: "container-name"
  paths:
    - "/path/to/watch/1"
    - "/path/to/watch/2"
status:
  state: "active"
  updateTime: "2024-01-01T00:00:00Z"
  statusTime: "2024-01-01T00:00:00Z"
```

### 字段说明

- **spec.pod**: Pod 名称（用于标识来源）
- **spec.container**: 容器名称（用于标识来源）
- **spec.paths**: 要监控的文件系统路径列表
- **status.state**: 资源状态
- **status.updateTime**: 更新时间
- **status.statusTime**: 状态时间

## Redis 事件发布

### 发布配置

- **Channel**: `"jfsnotify"` (常量 `producer.ChannelName`)
- **格式**: JSON 数组
- **内容**: `jfsnotify.Event` 数组

### 事件格式

```json
[
  {
    "Name": "/path/to/file",
    "Op": 2,
    "Key": "/path/to"
  }
]
```

**字段说明：**

- **Name**: 完整文件路径
- **Op**: 操作类型（位掩码）
  - `1`: Create
  - `2`: Write
  - `4`: Remove
  - `8`: Rename
  - `16`: Chmod
- **Key**: 父目录路径（用于路由）

### Redis 配置

**环境变量：**

- `REDIS_ADDR`: Redis 服务器地址（默认: `redis.kubesphere-system:6379`）
- `REDIS_PASSWORD`: Redis 密码（默认: 空）

**连接管理：**

- 启动时连接 Redis
- 支持上下文取消
- 优雅关闭时断开连接

## 并发和线程安全

### 组件并发模型

1. **App 初始化**: 在独立 goroutine 中运行文件系统监控
2. **Controller Worker**: 使用工作队列，支持多个 worker（当前为 1 个）
3. **事件处理**: 通过 channel 进行通信，线程安全

### 资源保护

- **Watcher 操作**: `fsnotify.Watcher` 本身是线程安全的
- **Redis 操作**: Redis 客户端是线程安全的
- **Lister 查询**: Kubernetes Lister 是线程安全的

## 错误处理和恢复

### 文件系统监控器

- **自动重启**: 监控器异常退出时会自动重新创建
- **错误记录**: 所有错误都会记录到日志
- **优雅关闭**: 收到停止信号时正常关闭

### Controller

- **重试机制**: 处理失败的任务会重新入队（带限流）
- **错误处理**: 使用 `utilruntime.HandleError` 统一处理错误
- **崩溃恢复**: 使用 `defer utilruntime.HandleCrash()` 防止 panic

### Redis 连接

- **连接检查**: 启动时通过 Ping 检查连接
- **上下文管理**: 使用 context 管理连接生命周期
- **优雅关闭**: 停止时正确关闭连接

## 信号处理

### 支持的信号

- `SIGTERM`: 终止信号
- `SIGINT`: 中断信号（Ctrl+C）

### 处理流程

1. **第一次信号**: 关闭 `stopCh`，触发优雅关闭
2. **第二次信号**: 直接退出（exit code 1）

### 优雅关闭顺序

1. Controller 停止处理新任务
2. 工作队列关闭
3. Informer Factory 关闭
4. 文件系统监控器关闭
5. Redis 连接关闭

## 使用场景

### 典型部署

1. **DaemonSet 部署**: 在每个节点上运行一个实例
2. **监控节点文件系统**: 监控节点上的特定路径
3. **事件发布**: 将文件系统事件发布到 Redis
4. **下游消费**: 其他服务通过 Redis 订阅接收事件

### 与其他组件的关系

```
Pod (JFSWatcher Client)
    ↓ (TCP/IP or IPC)
fsnotify-proxy
    ↓ (FSWatcher CRD)
fsnotify-daemon
    ↓ (fsnotify)
本地文件系统
    ↓ (事件)
Redis Pub/Sub
    ↓
下游消费者
```

## 配置说明

### 环境变量

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| `REDIS_ADDR` | Redis 服务器地址 | `redis.kubesphere-system:6379` |
| `REDIS_PASSWORD` | Redis 密码 | 空 |
| `REDIS_DB` | Redis 数据库编号 | 10 |

### 命令行参数

支持所有 klog 的标准参数，例如：
- `-v`: 日志级别
- `-add_dir_header`: 添加目录头
- 等等

## 监控和日志

### 日志级别

- **Info**: 正常操作信息
- **Error**: 错误信息
- **V(8)**: 详细调试信息（事件详情）

### 关键日志

- `"fsnotify-daemon starting ..."`: 服务启动
- `"fsnotify watcher stopping"`: 监控器停止
- `"handle add/update/delete object"`: Controller 事件处理
- `"fsnotify event: ..."`: 文件系统事件（V(8)）
- `"publish event error"`: 发布事件错误

## 性能考虑

### 优化点

1. **路径去重**: fsnotify 自动处理重复路径
2. **批量处理**: Controller 使用工作队列批量处理
3. **异步发布**: Redis 发布是异步的
4. **缓存查询**: 使用 Lister 缓存查询，减少 API 调用

### 限制

1. **单 Worker**: 当前只使用 1 个 worker 处理 Controller 任务
2. **全量同步**: 每次变更都重新同步所有路径
3. **事件缓冲**: 依赖 fsnotify 的内部缓冲

## 故障排查

### 常见问题

1. **Redis 连接失败**
   - 检查 Redis 服务是否运行
   - 检查网络连接
   - 检查认证信息

2. **文件系统监控失败**
   - 检查路径是否存在
   - 检查权限
   - 查看日志中的错误信息

3. **Controller 不工作**
   - 检查 RBAC 权限
   - 检查 FSWatcher CRD 是否注册
   - 查看 Controller 日志

### 调试技巧

1. **增加日志级别**: 使用 `-v=8` 查看详细日志
2. **检查资源**: `kubectl get fswatchers -A`
3. **检查事件**: 监听 Redis channel `jfsnotify`
4. **检查监控路径**: 查看 fsnotify 的监控列表

## 与 v1 版本的差异

### v1 (app/app.go)

- 使用 IPC 与 JuiceFS 通信
- 需要 JuiceFS 支持
- 通过 IPC 接收事件

### v2 (app/v2/app.go)

- 使用本地 fsnotify
- 不依赖 JuiceFS
- 直接监控本地文件系统
- 更简单，更通用

## 总结

`fsnotify-daemon` 是一个设计良好的 Kubernetes DaemonSet 服务，通过以下方式实现文件系统监控：

1. **CRD 驱动**: 通过 FSWatcher CRD 动态管理监控路径
2. **本地监控**: 使用 fsnotify 直接监控文件系统
3. **事件发布**: 通过 Redis Pub/Sub 发布事件
4. **优雅关闭**: 支持信号处理和优雅关闭
5. **自动恢复**: 支持组件异常时的自动恢复

该服务适合在 Kubernetes 集群中作为文件系统监控的基础设施组件使用。

