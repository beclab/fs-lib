# fsnotify-proxy 流程文档

## 概述

`fsnotify-proxy` 是一个运行在 Kubernetes 集群中的代理服务，作为文件系统监控系统的中间层。它接收来自 Pod 中 JFSWatcher 客户端的连接，订阅 Redis 中的文件系统事件，并将事件转发给相应的客户端。同时，它还负责管理 FSWatcher CRD 资源，实现 Pod 路径到节点路径的映射。

## 架构设计

### 核心组件

1. **Main Entry**: 程序入口，初始化所有组件
2. **App**: 应用主逻辑，管理代理服务器和消息处理
3. **Multicast Server**: TCP 服务器，管理客户端连接
4. **Subscriber**: Redis 订阅者，接收文件系统事件
5. **Watcher Helper**: 客户端辅助对象，管理路径映射和事件转发
6. **Kubernetes Clients**: 用于操作 FSWatcher CRD 和 Pod 资源

### 数据流向

```
Pod (JFSWatcher Client)
    ↓ (TCP 连接)
fsnotify-proxy
    ↓ (订阅)
Redis Pub/Sub
    ↑ (发布)
fsnotify-daemon
    ↑ (监控)
节点文件系统
```

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
    k8sClient := kubernetes.NewForConfigOrDie(config)
    
    // 5. 获取监听地址
    addr := os.Getenv("MULTICAST_SERVER")
    if addr == "" {
        addr = ":5079"  // 默认端口
    }
    
    // 6. 创建 App 实例
    app := app.New(apiCtx, stopCh, sysClient, k8sClient, addr)
    
    // 7. 启动服务
    app.Start()
}
```

**关键步骤：**

1. **日志初始化**: 使用 klog 进行日志记录
2. **Kubernetes 配置**: 通过 controller-runtime 获取集群配置
3. **信号处理**: 监听 SIGTERM 和 SIGINT 信号，优雅关闭
4. **客户端创建**: 
   - 创建自定义资源客户端（FSWatcher CRD）
   - 创建 Kubernetes 核心客户端（Pod 资源）
5. **监听地址**: 从环境变量 `MULTICAST_SERVER` 获取，默认 `:5079`
6. **App 初始化**: 创建应用实例并启动服务

### 2. App 初始化 (`app.New()`)

```go
func New(ctx, stopCh, clientSet, k8sClient, addr) *App
```

**初始化内容：**

1. **Multicast Server 创建**
   - 创建 TCP 服务器监听客户端连接
   - 创建 Redis 订阅者接收文件系统事件
   - 注册消息处理回调

2. **回调函数注册**
   - `ChannelMessageProc`: 处理客户端消息
   - `InitClient`: 初始化新客户端连接

3. **客户端初始化逻辑**
   - 为每个新连接创建 `watcher` 辅助对象
   - 设置清理函数（连接关闭时删除 FSWatcher CRD）

## 核心流程

### 1. 客户端连接管理

#### 1.1 新客户端连接

**流程：**

1. **TCP 连接建立**: 客户端连接到代理服务器
2. **生成客户端 ID**: 使用 UUID 生成唯一标识
3. **创建 Watcher Helper**: 
   ```go
   w := NewWatcher(c)
   w.Remove = func() {
       // 删除 FSWatcher CRD
   }
   ```
4. **注册到服务器**: 将客户端添加到 `watchClients` 映射

#### 1.2 客户端断开

**流程：**

1. **检测连接关闭**: TCP 服务器检测到连接断开
2. **清理资源**:
   - 调用 `watcher.Close()` 删除 FSWatcher CRD
   - 从 `watchClients` 映射中移除
3. **记录日志**: 记录连接关闭信息

### 2. 客户端消息处理 (`processWatcherMessage()`)

#### 2.1 消息解析

**消息格式：**
```
[4 bytes: 消息类型] + [消息数据]
```

**支持的消息类型：**

- `MSG_CLEAR` (3): 清除监控
- `MSG_WATCH` (1): 添加监控路径
- `MSG_UNWATCH` (2): 移除监控路径

#### 2.2 MSG_CLEAR 处理

**流程：**

1. 解析 Channel ID（watcher 标识）
2. 查找对应的 FSWatcher CRD
3. 删除 FSWatcher CRD 资源
4. 完成清理

#### 2.3 MSG_WATCH / MSG_UNWATCH 处理

**完整流程：**

1. **解析消息**
   ```go
   // 消息格式: [255 bytes: channelId] + [255 bytes: path1] + [255 bytes: path2] + ...
   channelId, keys := parseMsg()
   ```

2. **解析 Channel ID**
   ```go
   // Channel ID 格式: namespace/podname/containername
   namespace, podname, containername := getPodName(channelId)
   ```

3. **获取 Pod 信息**
   - 通过 Kubernetes API 获取 Pod 资源
   - 提取 Volume 和 VolumeMount 信息

4. **路径映射转换**
   - 构建 Volume 映射表（Volume Name -> HostPath）
   - 遍历容器的 VolumeMount
   - 将 Pod 路径转换为节点路径：
     ```go
     // Pod 路径: /mnt/data/file
     // 节点路径: /host/path/data/file
     podPathMap[nodePath] = podPath
     ```

5. **更新 FSWatcher CRD**
   - **资源不存在**: 创建新的 FSWatcher CRD
   - **资源已存在**: 更新 FSWatcher CRD 的路径列表

**路径映射示例：**

```
Pod Volume:
  - name: data
    hostPath:
      path: /host/path/data

Container VolumeMount:
  - name: data
    mountPath: /mnt/data

映射关系:
  Pod 路径: /mnt/data/file.txt
  节点路径: /host/path/data/file.txt
```

### 3. 事件转发流程

#### 3.1 Redis 事件订阅

**订阅配置：**

- **Channel**: `"jfsnotify"` (常量 `producer.ChannelName`)
- **格式**: JSON 数组，包含 `jfsnotify.Event` 对象

**事件格式：**
```json
[
  {
    "Name": "/host/path/data/file.txt",
    "Op": 2,
    "Key": "/host/path/data"
  }
]
```

#### 3.2 事件多播 (`multicastMsg()`)

**流程：**

1. **接收 Redis 消息**: Subscriber 接收到文件系统事件
2. **遍历所有客户端**: 获取所有已连接的客户端
3. **转发事件**: 对每个客户端调用 `WriteMsg()`

#### 3.3 事件处理 (`watcher.WriteMsg()`)

**处理步骤：**

1. **解析 JSON**: 将 Redis 消息解析为事件数组
2. **事件过滤和转换**:
   - 根据 `podPathMap` 判断事件是否属于该 Pod
   - 将节点路径转换回 Pod 路径
3. **Write 事件去重**: 
   - 使用 `delayWriteMsgs` 延迟处理
   - 1 秒内相同路径的 Write 事件只处理一次
4. **发送给客户端**: 将转换后的事件发送给客户端

**路径转换示例：**

```
节点事件: /host/path/data/file.txt (Key: /host/path/data)
Pod 路径映射: /host/path/data -> /mnt/data
转换后: /mnt/data/file.txt (Key: /mnt/data)
```

#### 3.4 Write 事件去重机制

**目的**: 避免频繁的 Write 事件导致客户端过载

**实现：**

```go
// 第一次收到 Write 事件
if !exists {
    // 延迟 1 秒发送
    delay := time.NewTimer(time.Second)
    go func() {
        <-delay.C
        // 检查是否还有新的 Write 事件
        if time.Since(lastTime) < time.Second {
            // 继续延迟
            send(event)
        } else {
            // 发送事件
            sendEvent(event)
        }
    }()
}
```

## FSWatcher CRD 管理

### CRD 创建

**触发条件：**

- 客户端发送 `MSG_WATCH` 消息
- 对应的 FSWatcher CRD 不存在

**创建内容：**

```yaml
apiVersion: sys.bytetrade.io/v1alpha1
kind: FSWatcher
metadata:
  name: <podname>-<containername>
  namespace: <namespace>
spec:
  pod: <podname>
  container: <containername>
  paths:
    - <node-path-1>
    - <node-path-2>
```

### CRD 更新

**触发条件：**

- 客户端发送 `MSG_WATCH` 或 `MSG_UNWATCH` 消息
- 对应的 FSWatcher CRD 已存在

**更新内容：**

- 更新 `spec.paths` 字段为当前所有监控路径

### CRD 删除

**触发条件：**

- 客户端发送 `MSG_CLEAR` 消息
- 客户端连接断开

**删除操作：**

```go
clientSet.SysV1alpha1().FSWatchers(namespace).Delete(ctx, name, metav1.DeleteOptions{})
```

## 路径映射机制

### 映射表结构

```go
type watcher struct {
    podPathMap map[string]string  // { nodePath: podPath }
}
```

### 映射规则

1. **Volume 类型**: 仅处理 `HostPath` 类型的 Volume
2. **Volume 子类型**: 仅处理 `Directory` 或 `DirectoryOrCreate`
3. **路径匹配**: 使用 `strings.HasPrefix` 匹配路径前缀
4. **路径拼接**: 处理路径分隔符，确保正确拼接

### 映射示例

**Pod 配置：**
```yaml
volumes:
  - name: data
    hostPath:
      path: /var/lib/data
      type: Directory

containers:
  - name: app
    volumeMounts:
      - name: data
        mountPath: /app/data
```

**映射结果：**
```
节点路径: /var/lib/data/file.txt
Pod 路径: /app/data/file.txt

podPathMap["/var/lib/data/file.txt"] = "/app/data/file.txt"
```

## 并发安全

### 锁机制

1. **Server 锁**: `sync.RWMutex` 保护 `watchClients` 映射
2. **Watcher 锁**: `sync.RWMutex` 保护 `podPathMap` 和 `delayWriteMsgs`

### 并发场景

1. **多客户端连接**: 每个客户端独立处理，互不干扰
2. **事件转发**: 使用读锁遍历客户端，异步发送消息
3. **路径映射更新**: 使用写锁保护映射表的更新

## 错误处理

### 消息处理错误

- **解析错误**: 记录日志，跳过当前消息
- **Pod 查询失败**: 记录日志，返回错误
- **CRD 操作失败**: 记录日志，返回错误

### 连接错误

- **客户端断开**: 自动清理资源
- **发送失败**: 记录日志，不影响其他客户端

### Redis 订阅错误

- **连接失败**: 启动时 panic（关键组件）
- **消息解析失败**: 记录日志，跳过消息

## 配置说明

### 环境变量

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| `MULTICAST_SERVER` | TCP 服务器监听地址 | `:5079` |
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

- **Info**: 正常操作信息（连接、消息处理等）
- **Error**: 错误信息（连接失败、处理错误等）
- **V(8)**: 详细调试信息

### 关键日志

- `"fsnotify-proxy starting ..."`: 服务启动
- `"channel closed"`: 客户端断开
- `"watcher cleared"`: 监控清除
- `"watcher <id> send path <paths>"`: 路径注册
- `"translate msg to watcher"`: 事件转换
- `"send msg to watcher"`: 事件发送
- `"subcribed msg"`: Redis 消息接收

## 性能考虑

### 优化点

1. **Write 事件去重**: 减少频繁 Write 事件的传输
2. **异步消息发送**: 事件转发使用 goroutine 异步处理
3. **路径映射缓存**: 在 watcher 中缓存路径映射关系
4. **读锁优化**: 使用读锁遍历客户端列表

### 限制

1. **单点故障**: 代理服务器是单点，需要高可用部署
2. **内存占用**: 每个客户端连接都会占用内存
3. **路径映射**: 需要查询 Kubernetes API，可能有延迟

## 故障排查

### 常见问题

1. **客户端连接失败**
   - 检查网络连接
   - 检查端口是否开放
   - 检查防火墙规则

2. **事件未收到**
   - 检查 Redis 连接
   - 检查路径映射是否正确
   - 检查 FSWatcher CRD 是否创建

3. **路径映射错误**
   - 检查 Pod 的 Volume 配置
   - 检查 VolumeMount 配置
   - 查看日志中的路径信息

4. **CRD 操作失败**
   - 检查 RBAC 权限
   - 检查 FSWatcher CRD 是否注册
   - 检查命名空间权限

### 调试技巧

1. **增加日志级别**: 使用 `-v=8` 查看详细日志
2. **检查连接**: 使用 `netstat` 或 `ss` 查看连接状态
3. **检查 CRD**: `kubectl get fswatchers -A`
4. **检查 Redis**: 监听 Redis channel `jfsnotify`
5. **检查 Pod**: `kubectl get pod <podname> -o yaml`

## 与其他组件的关系

### 与 JFSWatcher 客户端

- **连接**: JFSWatcher 客户端通过 TCP 连接到代理服务器
- **消息**: 客户端发送 WATCH/UNWATCH/CLEAR 消息
- **事件**: 代理服务器将事件转发给客户端

### 与 fsnotify-daemon

- **事件源**: daemon 监控文件系统并发布事件到 Redis
- **事件消费**: proxy 订阅 Redis 并转发事件

### 与 Kubernetes

- **资源管理**: 管理 FSWatcher CRD 资源
- **Pod 查询**: 查询 Pod 信息进行路径映射

## 数据流图

```
┌─────────────────┐
│  Pod (Client)   │
│  JFSWatcher     │
└────────┬────────┘
         │ TCP 连接
         │ WATCH/UNWATCH/CLEAR
         ↓
┌─────────────────┐
│ fsnotify-proxy  │
│                 │
│ 1. 接收客户端消息│
│ 2. 路径映射转换  │
│ 3. 管理 CRD     │
│ 4. 订阅 Redis   │
│ 5. 转发事件     │
└────────┬────────┘
         │
         │ 订阅
         ↓
┌─────────────────┐
│     Redis       │
│  Channel: jfs   │
└────────┬────────┘
         │
         │ 发布
         ↓
┌─────────────────┐
│fsnotify-daemon  │
│  监控文件系统    │
└─────────────────┘
```

## 总结

`fsnotify-proxy` 是一个关键的中间层组件，它：

1. **桥接客户端和服务端**: 连接 Pod 中的客户端和节点上的 daemon
2. **路径映射**: 实现 Pod 路径到节点路径的转换
3. **资源管理**: 自动管理 FSWatcher CRD 资源
4. **事件转发**: 将文件系统事件从 Redis 转发给相应的客户端
5. **事件优化**: 通过去重机制优化 Write 事件的传输

该服务适合在 Kubernetes 集群中作为文件系统监控的代理层使用，提供了 Pod 和节点之间的路径转换和事件路由功能。

