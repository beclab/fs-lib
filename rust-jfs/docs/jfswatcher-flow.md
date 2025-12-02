# JFSWatcher 流程文档

## 概述

JFSWatcher 是一个基于客户端-服务器架构的文件系统监控组件，用于在 Kubernetes 集群环境中监控文件系统变化。它通过 TCP/IP 或 IPC 连接与远程服务器通信，实现分布式文件系统事件监控。

## 架构设计

### 核心组件

1. **Watcher 结构体**: 主要的监控器实现
2. **通信协议**: 基于 goframe 的帧协议
3. **消息类型**: 定义了多种消息类型用于客户端-服务器通信
4. **事件处理**: 异步事件接收和分发机制

### 环境变量配置

JFSWatcher 通过以下环境变量进行配置：

- `POD_NAME` / `HOSTNAME`: Pod 名称（默认: "default"）
- `NAMESPACE`: Kubernetes 命名空间（默认: "default"）
- `NOTIFY_SERVER`: 服务器地址（默认: "fsnotify-svc.os-system:1506"）
- `CONTAINER_NAME`: 容器名称（默认: "default"）
- `FS_TYPE`: 文件系统类型，支持 "jfs" 和 "fs"（默认: "jfs"）

## 初始化流程

### 1. 创建 Watcher (`NewWatcher`)

```go
watcher, err := NewWatcher("watcher-name")
```

**流程步骤：**

1. 检查 `FS_TYPE` 环境变量
2. 根据类型选择实现：
   - `"jfs"`: 创建 JFSWatcher（远程监控）
   - `"fs"`: 创建 FSWatcher（本地监控）
3. 验证 watcher 名称（不能包含 "/" 字符）

### 2. JFSWatcher 初始化 (`newJFSWatcher`)

**初始化内容：**

- 创建事件通道 (`Events chan Event`)
- 创建错误通道 (`Errors chan error`)
- 初始化任务队列 (`initTask chan func()`)
- 初始化重连通道 (`reconnect chan int`)
- 初始化发送队列 (`sendQ chan []byte`)
- 生成唯一标识符: `{podName}/{containerName}/{name}`
- 启动主循环 (`start()` goroutine)

## 主循环流程 (`start()`)

### 连接建立阶段

1. **连接尝试**
   - 解析服务器地址 (`parseDialTarget`)
   - 支持协议类型：
     - `tcp`: TCP 连接
     - `unix`: Unix 域套接字
     - `ipc`: IPC 连接
   - 连接失败时等待 1 秒后重试

2. **帧协议配置**
   ```go
   EncoderConfig:
     - ByteOrder: BigEndian
     - LengthFieldLength: 4
     - LengthAdjustment: 0
     - LengthIncludesLengthFieldLength: false
   
   DecoderConfig:
     - ByteOrder: BigEndian
     - LengthFieldOffset: 0
     - LengthFieldLength: 4
     - LengthAdjustment: 0
     - InitialBytesToStrip: 4
   ```

3. **初始化任务**
   - 发送 `MSG_CLEAR` 消息清除之前的监控状态
   - 重新发送所有已注册的监控路径

### 消息处理流程

#### 发送流程

1. **发送队列处理** (独立 goroutine)
   - 从 `sendQ` 通道接收数据
   - 使用 `syncWrite()` 同步写入（带锁保护）
   - 错误处理：
     - 如果是套接字错误，触发重连
     - 其他错误发送到 `Errors` 通道

2. **消息打包格式**
   ```
   [4 bytes: 消息类型] + [消息数据]
   ```

#### 接收流程

1. **事件接收** (独立 goroutine)
   - 持续从连接读取帧数据 (`ReadFrame()`)
   - 解析消息 (`UnpackMsg()`)
   - 验证消息类型为 `MSG_EVENT`
   - 解析事件数组 (`UnpackEvent()`)
   - 将事件发送到 `Events` 通道

2. **事件格式**
   ```
   每个事件: [255 bytes: 路径] + [4 bytes: 操作类型] + [255 bytes: Key]
   ```

### 重连机制

1. **触发条件**
   - 套接字错误 (`isSocketError()`)
   - 读取/写入失败
   - 手动触发 (`reconnect` 通道)

2. **重连流程**
   - 清空 `reconnect` 通道中的所有信号
   - 关闭当前连接 (`fconn.Close()`)
   - 清空连接引用 (`fconn = nil`)
   - 返回主循环重新连接

## 操作接口

### Add - 添加监控路径

**流程：**

1. 调用 `internelAdd()` 或 `internelAddWith()`
2. 发送 `MSG_WATCH` 消息到服务器
3. 将路径添加到本地 `watches` 列表（带锁保护）

**消息格式：**
```
[255 bytes: watcher name] + [255 bytes: path1] + [255 bytes: path2] + ...
```

**Watcher Name 格式：**
```
{namespace}/{podName}/{containerName}/{watcherName}
```

### Remove - 移除监控路径

**流程：**

1. 调用 `internelRemove()`
2. 发送 `MSG_UNWATCH` 消息到服务器
3. 从本地 `watches` 列表中移除路径（带锁保护）

### WatchList - 获取监控列表

**流程：**

1. 调用 `internelWatchList()`
2. 返回当前 `watches` 列表的副本（带读锁保护）

### Close - 关闭 Watcher

**流程：**

1. 调用 `internelClose()`
2. 取消上下文 (`close()`)
3. 清理资源 (`clear()`):
   - 关闭 `Errors` 通道
   - 关闭 `initTask` 通道
   - 关闭 `reconnect` 通道
   - 关闭 `sendQ` 通道

## 消息类型

### MSG_WATCH (1)

**用途**: 添加监控路径

**数据格式：**
```
[255 bytes: watcher name]
[255 bytes: path1]
[255 bytes: path2]
...
```

### MSG_UNWATCH (2)

**用途**: 移除监控路径

**数据格式：**
```
[255 bytes: watcher name]
[255 bytes: path1]
[255 bytes: path2]
...
```

### MSG_CLEAR (3)

**用途**: 清除所有监控（启动时发送）

**数据格式：**
```
[255 bytes: watcher name]
```

### MSG_EVENT (6)

**用途**: 服务器发送的文件系统事件

**数据格式：**
```
[255 bytes: path] + [4 bytes: op] + [255 bytes: key]
[255 bytes: path] + [4 bytes: op] + [255 bytes: key]
...
```

## 事件类型

### Op 操作类型

- `Create` (1 << 0): 文件/目录创建
- `Write` (1 << 1): 文件写入
- `Remove` (1 << 2): 文件/目录删除
- `Rename` (1 << 3): 文件/目录重命名
- `Chmod` (1 << 4): 属性变更

### Event 结构

```go
type Event struct {
    Name string  // 文件路径
    Op   Op      // 操作类型
    Key  string  // 关联键值
}
```

## 并发安全

### 锁机制

1. **mu (RWMutex)**: 保护 `watches` 列表的读写
2. **writeMu (Mutex)**: 保护 `syncWrite()` 的并发写入

### 通道通信

- `Events`: 无缓冲通道，用于事件分发
- `Errors`: 无缓冲通道，用于错误报告
- `sendQ`: 缓冲通道（255），用于异步发送
- `initTask`: 缓冲通道（10），用于初始化任务
- `reconnect`: 缓冲通道（10），用于重连信号

## 错误处理

### 错误类型

1. **套接字错误**: 触发自动重连
2. **协议错误**: 发送到 `Errors` 通道
3. **解析错误**: 记录日志并跳过

### 错误判断

`isSocketError()` 函数判断是否为套接字错误：
- 排除: `ErrTooLessLength`, `ErrUnexpectedFixedLength`, `ErrUnsupportedlength`
- 其他错误视为套接字错误

## 使用示例

```go
// 创建 watcher
watcher, err := NewWatcher("my-watcher")
if err != nil {
    log.Fatal(err)
}
defer watcher.Close()

// 添加监控路径
err = watcher.Add("/path/to/watch")
if err != nil {
    log.Fatal(err)
}

// 监听事件
go func() {
    for {
        select {
        case event := <-watcher.Events:
            fmt.Printf("Event: %s\n", event)
        case err := <-watcher.Errors:
            fmt.Printf("Error: %v\n", err)
        }
    }
}()

// 保持运行
select {}
```

## 关键设计点

1. **自动重连**: 网络断开时自动重连，保证服务可用性
2. **状态同步**: 重连后自动重新发送所有监控路径
3. **异步处理**: 使用 goroutine 和通道实现异步事件处理
4. **线程安全**: 使用锁保护共享状态
5. **资源清理**: 关闭时正确清理所有资源

## 注意事项

1. Watcher 名称不能包含 "/" 字符
2. 路径长度限制为 255 字节
3. 服务器地址格式支持多种协议（tcp://, unix://, ipc://）
4. 事件通道需要及时消费，避免阻塞
5. 在 Kubernetes 环境中需要正确设置环境变量

