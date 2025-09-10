use crossbeam_channel::{bounded, Receiver, Sender};
use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::Duration;

/// 业务侧入口：管理双channel的TCP读写和重连
fn main() {
    // 创建两个channel：一个用于发送数据到TCP，一个用于接收TCP数据
    let (write_tx, write_rx) = bounded::<Vec<u8>>(32);
    let (read_tx, read_rx) = bounded::<Vec<u8>>(32);

    // 模拟业务：每秒发一条心跳到写channel
    let write_tx_clone = write_tx.clone();
    thread::spawn(move || {
        let mut cnt = 0usize;
        loop {
            thread::sleep(Duration::from_secs(1));
            let message = format!("ping-{}\n", cnt);
            if write_tx_clone.send(message.into_bytes()).is_err() {
                println!("Write channel closed, stopping heartbeat");
                break;
            }
            cnt += 1;
        }
    });

    // 模拟业务：处理从TCP接收到的数据
    let read_rx_clone = read_rx.clone();
    thread::spawn(move || {
        while let Ok(data) = read_rx_clone.recv() {
            match std::str::from_utf8(&data) {
                Ok(text) => println!("Received: {}", text.trim()),
                Err(_) => println!("Received binary data: {:?}", data),
            }
        }
        println!("Read channel closed, stopping data handler");
    });

    // 将重连循环放到独立线程中，让主线程非阻塞
    let reconnect_handle = thread::spawn(move || {
        let mut retry_count = 0;
        const MAX_RETRIES: u32 = 10;

        loop {
            println!("Attempting to connect (attempt {})...", retry_count + 1);

            match connect_and_run(write_rx.clone(), read_tx.clone()) {
                Ok(()) => {
                    println!("Connection closed by server, will reconnect...");
                    retry_count = 0; // 重置重试计数，因为连接曾经成功过
                }
                Err(e) => {
                    eprintln!("Connection failed: {}, retrying...", e);
                    retry_count += 1;

                    if retry_count > MAX_RETRIES {
                        eprintln!("Max retries ({}) reached, giving up.", MAX_RETRIES);
                        break;
                    }

                    // 指数退避重试
                    let wait_time = Duration::from_millis(200 * 2_u64.pow(retry_count.min(6)));
                    println!("Waiting {:?} before retry {}...", wait_time, retry_count);
                    thread::sleep(wait_time);
                }
            }
        }

        println!("Reconnect thread exiting...");
    });

    // 主线程现在可以处理其他任务
    println!("Main thread: TCP client started, reconnect thread is running...");
    println!("Main thread: Press Ctrl+C to exit");

    // 主线程可以在这里处理其他任务
    // 例如：处理用户输入、监控系统状态、处理其他业务逻辑等
    loop {
        // 模拟主线程的其他工作
        thread::sleep(Duration::from_secs(5));
        println!(
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
    reconnect_handle.join().unwrap();

    println!("Program exiting...");
}

/// 建立TCP连接并运行读写线程
fn connect_and_run(
    write_rx: Receiver<Vec<u8>>,
    read_tx: Sender<Vec<u8>>,
) -> Result<(), Box<dyn std::error::Error>> {
    let addr = "127.0.0.1:8080";
    println!("Connecting to {}...", addr);

    let stream = TcpStream::connect(addr)?;
    stream.set_nonblocking(false)?; // 使用阻塞模式

    // 克隆TCP流用于读写
    let mut stream_read = stream.try_clone()?;
    let mut stream_write = stream.try_clone()?;

    // 创建连接状态标志
    let connection_alive = Arc::new(AtomicBool::new(true));
    let connection_alive_clone = Arc::clone(&connection_alive);

    // 读线程：从TCP读取数据并发送到读channel
    let read_handle = thread::spawn(move || {
        let mut buffer = vec![0; 1024];

        while connection_alive_clone.load(Ordering::Relaxed) {
            match stream_read.read(&mut buffer) {
                Ok(0) => {
                    println!("Server closed connection");
                    connection_alive_clone.store(false, Ordering::Relaxed);
                    break;
                }
                Ok(n) => {
                    let data = buffer[..n].to_vec();
                    if read_tx.send(data).is_err() {
                        println!("Read channel closed, stopping read thread");
                        break;
                    }
                }
                Err(e) => {
                    eprintln!("Read error: {}", e);
                    connection_alive_clone.store(false, Ordering::Relaxed);
                    break;
                }
            }
        }
    });

    // 写线程：从写channel接收数据并写入TCP
    let write_handle = thread::spawn(move || {
        while connection_alive.load(Ordering::Relaxed) {
            match write_rx.recv_timeout(Duration::from_millis(100)) {
                Ok(data) => {
                    if stream_write.write_all(&data).is_err() {
                        eprintln!("Write error, connection may be lost");
                        connection_alive.store(false, Ordering::Relaxed);
                        break;
                    }
                    if stream_write.flush().is_err() {
                        eprintln!("Flush error, connection may be lost");
                        connection_alive.store(false, Ordering::Relaxed);
                        break;
                    }
                }
                Err(crossbeam_channel::RecvTimeoutError::Timeout) => {
                    // 超时是正常的，继续循环检查连接状态
                    continue;
                }
                Err(crossbeam_channel::RecvTimeoutError::Disconnected) => {
                    println!("Write channel closed, stopping write thread");
                    break;
                }
            }
        }
    });

    // 使用 try_join 来避免阻塞，只要有一个线程结束就返回
    loop {
        // 尝试非阻塞地等待读线程
        if read_handle.is_finished() {
            let read_result = read_handle.join();
            if read_result.is_err() {
                eprintln!("Read thread panicked");
            } else {
                println!("Read thread ended, connection lost");
            }
            // 等待写线程结束
            let _ = write_handle.join();
            break;
        }

        // 尝试非阻塞地等待写线程
        if write_handle.is_finished() {
            let write_result = write_handle.join();
            if write_result.is_err() {
                eprintln!("Write thread panicked");
            } else {
                println!("Write thread ended, connection lost");
            }
            // 等待读线程结束
            let _ = read_handle.join();
            break;
        }

        // 短暂休眠避免忙等待
        thread::sleep(Duration::from_millis(10));
    }

    Ok(())
}
