use crossbeam_channel::{bounded, Receiver, Sender};
use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;
use tokio::time::sleep;
use byteorder::{BigEndian, ReadBytesExt, WriteBytesExt};
use anyhow::Result;

/// 读取长度前缀的数据帧
fn read_length_prefixed_frame(stream: &mut TcpStream) -> Result<Vec<u8>> {
    // 先读取4字节的长度字段（大端序）
    let length = stream.read_u32::<BigEndian>()? as usize;
    
    // 读取指定长度的数据
    let mut buffer = vec![0u8; length];
    stream.read_exact(&mut buffer)?;
    
    Ok(buffer)
}

/// 写入长度前缀的数据帧
fn write_length_prefixed_frame(stream: &mut TcpStream, data: &[u8]) -> Result<()> {
    // 先写入4字节的长度字段（大端序，不包含长度字段本身）
    stream.write_u32::<BigEndian>(data.len() as u32)?;
    
    // 写入实际数据
    stream.write_all(data)?;
    stream.flush()?;
    
    Ok(())
}

/// 业务侧入口：管理双channel的TCP读写和重连
#[tokio::main]
async fn main() {
    // 初始化日志系统
    env_logger::init();
    
    // 创建两个channel：一个用于发送数据到TCP，一个用于接收TCP数据
    let (write_tx, write_rx) = bounded::<Vec<u8>>(32);
    let (read_tx, read_rx) = bounded::<Vec<u8>>(32);

    // 模拟业务：每秒发一条心跳到写channel
    let write_tx_clone = write_tx.clone();
    tokio::spawn(async move {
        let mut cnt = 0usize;
        loop {
            sleep(Duration::from_secs(1)).await;
            let message = format!("ping-{}\n", cnt);
            if write_tx_clone.send(message.into_bytes()).is_err() {
                log::info!("Write channel closed, stopping heartbeat");
                break;
            }
            cnt += 1;
        }
    });

    // 模拟业务：处理从TCP接收到的数据
    let read_rx_clone = read_rx.clone();
    tokio::spawn(async move {
        while let Ok(data) = read_rx_clone.recv() {
            match std::str::from_utf8(&data) {
                Ok(text) => log::info!("Received: {}", text.trim()),
                Err(_) => log::info!("Received binary data: {:?}", data),
            }
        }
        log::info!("Read channel closed, stopping data handler");
    });

    // 将重连循环放到独立线程中，让主线程非阻塞
    let reconnect_handle = tokio::spawn(async move {
        let mut retry_count = 0;
        const MAX_RETRIES: u32 = 10;

        loop {
            log::info!("Attempting to connect (attempt {})...", retry_count + 1);

            match connect_and_run(write_rx.clone(), read_tx.clone()).await {
                Ok(()) => {
                    log::info!("Connection closed by server, will reconnect...");
                    retry_count = 0; // 重置重试计数，因为连接曾经成功过
                }
                Err(e) => {
                    log::error!("Connection failed: {}, retrying...", e);
                    retry_count += 1;

                    if retry_count > MAX_RETRIES {
                        log::error!("Max retries ({}) reached, giving up.", MAX_RETRIES);
                        break;
                    }

                    // 指数退避重试
                    let wait_time = Duration::from_millis(200 * 2_u64.pow(retry_count.min(6)));
                    log::info!("Waiting {:?} before retry {}...", wait_time, retry_count);
                    sleep(wait_time).await;
                }
            }
        }

        log::info!("Reconnect thread exiting...");
    });

    // 主线程现在可以处理其他任务
    log::info!("Main thread: TCP client started, reconnect thread is running...");
    log::info!("Main thread: Press Ctrl+C to exit");

    // 主线程可以在这里处理其他任务
    // 例如：处理用户输入、监控系统状态、处理其他业务逻辑等
    loop {
        // 模拟主线程的其他工作
        sleep(Duration::from_secs(5)).await;
        log::info!(
            "Main thread: Still running, reconnect thread status: {}",
            if reconnect_handle.is_finished() {
                "finished"
            } else {
                "running"
            }
        );

        // 如果重连线程结束了，主线程也退出
        if reconnect_handle.is_finished() {
            break;
        }
    }

    // 等待重连线程完全结束
    reconnect_handle.await.unwrap();

    log::info!("Program exiting...");
}

/// 建立TCP连接并运行读写线程
async fn connect_and_run(
    write_rx: Receiver<Vec<u8>>,
    read_tx: Sender<Vec<u8>>,
) -> Result<()> {
    let addr = "127.0.0.1:8080";
    log::info!("Connecting to {}...", addr);

    let stream = TcpStream::connect(addr)?;
    stream.set_nonblocking(false)?; // 使用阻塞模式

    // 克隆TCP流用于读写
    let mut stream_read = stream.try_clone()?;
    let mut stream_write = stream.try_clone()?;

    // 创建连接状态标志
    let connection_alive = Arc::new(AtomicBool::new(true));
    let connection_alive_clone = Arc::clone(&connection_alive);

    // 读线程：从TCP读取长度前缀的数据帧并发送到读channel
    let read_handle = tokio::spawn(async move {
        while connection_alive_clone.load(Ordering::Relaxed) {
            match read_length_prefixed_frame(&mut stream_read) {
                Ok(data) => {
                    if read_tx.send(data).is_err() {
                        log::info!("Read channel closed, stopping read thread");
                        break;
                    }
                }
                Err(e) => {
                    if e.downcast_ref::<std::io::Error>()
                        .map(|io_err| io_err.kind() == std::io::ErrorKind::UnexpectedEof)
                        .unwrap_or(false) {
                        log::info!("Server closed connection");
                    } else {
                        log::error!("Read error: {}", e);
                    }
                    connection_alive_clone.store(false, Ordering::Relaxed);
                    break;
                }
            }
        }
    });

    // 写线程：从写channel接收数据并写入长度前缀的TCP帧
    let write_handle = tokio::spawn(async move {
        while connection_alive.load(Ordering::Relaxed) {
            match write_rx.recv_timeout(Duration::from_millis(100)) {
                Ok(data) => {
                    if write_length_prefixed_frame(&mut stream_write, &data).is_err() {
                        log::error!("Write error, connection may be lost");
                        connection_alive.store(false, Ordering::Relaxed);
                        break;
                    }
                }
                Err(crossbeam_channel::RecvTimeoutError::Timeout) => {
                    // 超时是正常的，继续循环检查连接状态
                    continue;
                }
                Err(crossbeam_channel::RecvTimeoutError::Disconnected) => {
                    log::info!("Write channel closed, stopping write thread");
                    break;
                }
            }
        }
    });

    // 使用 try_join 来避免阻塞，只要有一个线程结束就返回
    loop {
        // 尝试非阻塞地等待读线程
        if read_handle.is_finished() {
            let read_result = read_handle.await;
            if read_result.is_err() {
                log::error!("Read thread panicked");
            } else {
                log::info!("Read thread ended, connection lost");
            }
            // 等待写线程结束
            let _ = write_handle.await;
            break;
        }

        // 尝试非阻塞地等待写线程
        if write_handle.is_finished() {
            let write_result = write_handle.await;
            if write_result.is_err() {
                log::error!("Write thread panicked");
            } else {
                log::info!("Write thread ended, connection lost");
            }
            // 等待读线程结束
            let _ = read_handle.await;
            break;
        }

        // 短暂休眠避免忙等待
        sleep(Duration::from_millis(10)).await;
    }

    Ok(())
}
