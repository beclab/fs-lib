use crate::errors::{JFSError, JFSResult};
use crate::event::Event;
use crate::ipc::{FrameConnection, FrameConnectionWrapper, IpcConnection, TcpConnection};
use crate::utils::{
    get_env_var, package_msg, parse_dial_target, unpack_events, unpack_msg, MessageType,
};
use async_trait::async_trait;
use bytes::{BufMut, BytesMut};
use crossbeam_channel::{bounded, Receiver, Sender};
use std::collections::HashSet;
use std::sync::Arc;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpStream;
use tokio::sync::broadcast;
use tokio::sync::{Mutex, RwLock};
use tokio::time::{sleep, Duration};

/// Configuration for the JFS watcher
#[derive(Debug, Clone)]
pub struct JFSConfig {
    pub pod_name: String,
    pub pod_namespace: String,
    pub server_addr: String,
    pub container_name: String,
    pub fs_type: String,
}

impl Default for JFSConfig {
    fn default() -> Self {
        JFSConfig {
            pod_name: get_env_var("POD_NAME", &["HOSTNAME"]),
            pod_namespace: get_env_var("NAMESPACE", &[]),
            server_addr: get_env_var("NOTIFY_SERVER", &[]),
            container_name: get_env_var("CONTAINER_NAME", &[]),
            fs_type: get_env_var("FS_TYPE", &[]),
        }
    }
}

/// Trait for watcher implementations
#[async_trait]
pub trait Watcher: Send + Sync {
    async fn add(&self, path: &str) -> JFSResult<()>;
    async fn add_with(&self, path: &str, _opts: &[AddOption]) -> JFSResult<()>;
    async fn remove(&self, path: &str) -> JFSResult<()>;
    async fn close(&self) -> JFSResult<()>;
    async fn watch_list(&self) -> JFSResult<Vec<String>>;

    fn events_receiver(&self) -> Receiver<Event>;
    fn errors_receiver(&self) -> Receiver<JFSError>;
}

/// Add options for watcher
#[derive(Debug, Clone)]
pub struct AddOption {
    pub buffer_size: Option<usize>,
}

impl AddOption {
    pub fn buffer_size(size: usize) -> Self {
        AddOption {
            buffer_size: Some(size),
        }
    }
}

/// JFS Watcher implementation
pub struct JFSWatcher {
    config: JFSConfig,
    name: String,
    watches: Arc<RwLock<HashSet<String>>>,
    events_tx: Sender<Event>,
    events_rx: Receiver<Event>,
    errors_tx: Sender<JFSError>,
    errors_rx: Receiver<JFSError>,
    // connection: Arc<Mutex<Option<Box<dyn FrameConnection>>>>,
    write_tx: Sender<Vec<u8>>,
    write_rx: Receiver<Vec<u8>>,
    shutdown_tx: Sender<()>,
    shutdown_rx: Receiver<()>,
    reconnect_tx: Sender<()>,
    reconnect_rx: Receiver<()>,
}

impl JFSWatcher {
    /// Create a new JFS watcher
    pub async fn new(name: &str) -> JFSResult<Self> {
        log::info!("Creating new JFS watcher for {}", &name);
        Self::with_config(name, JFSConfig::default()).await
    }

    /// Create a new JFS watcher with custom config
    pub async fn new_with_config(name: &str, config: JFSConfig) -> JFSResult<Self> {
        Self::with_config(name, config).await
    }

    async fn with_config(name: &str, config: JFSConfig) -> JFSResult<Self> {
        if name.contains('/') {
            return Err(JFSError::BadWatcherName);
        }

        let (events_tx, events_rx) = bounded(1000);
        let (errors_tx, errors_rx) = bounded(100);
        let (shutdown_tx, shutdown_rx) = bounded(1);
        let (reconnect_tx, reconnect_rx) = bounded(10);
        let (write_tx, write_rx) = bounded(100);

        let watcher_name = format!("{}/{}/{}", config.pod_name, config.container_name, name);

        let watcher = JFSWatcher {
            config,
            name: watcher_name,
            watches: Arc::new(RwLock::new(HashSet::new())),
            events_tx,
            events_rx,
            errors_tx,
            errors_rx,
            // connection: Arc::new(Mutex::new(None)),
            write_rx,
            write_tx,
            shutdown_tx,
            shutdown_rx,
            reconnect_tx,
            reconnect_rx,
        };

        log::info!("Starting connection manager for {}", &name);
        // Start the connection manager
        watcher.start_connection_manager();

        Ok(watcher)
    }

    /// Start the connection manager task
    pub async fn start_connection_manager(&self) {
        log::info!("11111111111111111111111111111");
        let config = self.config.clone();
        let name = self.name.clone();
        let watches = Arc::clone(&self.watches);
        // let connection = Arc::clone(&self.connection);
        let write_rx = self.write_rx.clone();
        let write_tx = self.write_tx.clone();
        let events_tx = self.events_tx.clone();
        let errors_tx = self.errors_tx.clone();
        let _shutdown_rx = self.shutdown_rx.clone();
        let reconnect_rx = self.reconnect_rx.clone();
        let reconnect_tx = self.reconnect_tx.clone();
        log::info!("22222222222222222222222222222");
        tokio::spawn(async move {
            let reconnect_rx = reconnect_rx;

            log::info!("33333333333333333333333333333");
            loop {
                // TODO: Implement proper shutdown handling
                // tokio::select! {
                //     _ = async { shutdown_rx.recv() } => {
                //         log::info!("Connection manager shutting down");
                //         break;
                //     }
                //     _ = async { reconnect_rx.recv() } => {
                //         log::info!("Reconnection requested");
                //     }
                // }

                log::info!("44444444444444444444444444444");
                log::info!("Trying to connect to {}", &config.server_addr);

                let (protocol, addr) = parse_dial_target(&config.server_addr).unwrap();

                /**
                    // Try to connect
                    match Self::connect_to_server(&config, &name).await {
                        Ok(conn) => {
                            log::info!("Successfully connected to {}", config.server_addr);

                            // Store the connection
                            {
                                let mut conn_guard = connection.lock().await;
                                *conn_guard = Some(conn);
                            }

                            // Start write handler
                            Self::start_write_handler(&connection, &write_tx);

                            // Send clear signal
                            if let Err(e) = Self::send_clear_signal(&write_tx, &name, &config).await {
                                log::error!("Failed to send clear signal: {}", e);
                            }

                            // Re-send all watches
                            if let Err(e) =
                                Self::resend_watches(&write_tx, &watches, &name, &config).await
                            {
                                log::error!("Failed to resend watches: {}", e);
                            }

                            // Start reading from connection
                            Self::start_reader(&connection, &events_tx, &errors_tx, &reconnect_tx);

                            // Wait for reconnection signal
                            let _ = async { reconnect_rx.recv() }.await;

                            // Close current connection
                            {
                                let mut conn_guard = connection.lock().await;
                                if let Some(mut conn) = conn_guard.take() {
                                    let _ = conn.shutdown().await;
                                }
                            }
                        }
                        Err(e) => {
                            log::error!("Failed to connect to {}: {}", config.server_addr, e);
                            sleep(Duration::from_secs(1)).await;
                        }
                    }
                */
                log::info!("55555555555555555555555555555");
            }
        });
    }

    /**
        /// Connect to the JFS server
        async fn connect_to_server(
            config: &JFSConfig,
            _name: &str,
        ) -> JFSResult<Box<dyn FrameConnection>> {
            let (protocol, addr) = parse_dial_target(&config.server_addr)?;

            match protocol.as_str() {
                "ipc" => {
                    let conn = IpcConnection::connect(&addr).await?;
                    let frame_conn = FrameConnectionWrapper::new(conn);
                    Ok(Box::new(frame_conn))
                }
                "tcp" | "unix" => {
                    let conn = TcpConnection::connect(&addr).await?;
                    let frame_conn = FrameConnectionWrapper::new(conn);
                    Ok(Box::new(frame_conn))
                }
                _ => Err(JFSError::ConnectionError(format!(
                    "Unsupported protocol: {}",
                    protocol
                ))),
            }
        }
    */
    /// Send clear signal to server
    async fn send_clear_signal(
        write_tx: &Sender<Vec<u8>>,
        name: &str,
        config: &JFSConfig,
    ) -> JFSResult<()> {
        let mut data = vec![0u8; 255];
        let clear_name = format!("{}/{}", config.pod_namespace, name);
        let name_bytes = clear_name.as_bytes();
        let copy_len = std::cmp::min(name_bytes.len(), 255);
        data[..copy_len].copy_from_slice(&name_bytes[..copy_len]);

        let msg = package_msg(MessageType::Clear, &data);

        write_tx
            .send(msg.to_vec())
            .map_err(|_| JFSError::ConnectionError("Failed to send clear signal".to_string()))?;

        Ok(())
    }

    /// Resend all watches to server
    async fn resend_watches(
        write_tx: &Sender<Vec<u8>>,
        watches: &Arc<RwLock<HashSet<String>>>,
        name: &str,
        config: &JFSConfig,
    ) -> JFSResult<()> {
        let watch_list = watches.read().await;
        if watch_list.is_empty() {
            return Ok(());
        }

        Self::send_watch_message(
            write_tx,
            &watch_list.iter().cloned().collect::<Vec<_>>(),
            name,
            config,
            MessageType::Watch,
        )
        .await
    }

    /// Send watch/unwatch message to server
    async fn send_watch_message(
        write_tx: &Sender<Vec<u8>>,
        watches: &[String],
        name: &str,
        config: &JFSConfig,
        msg_type: MessageType,
    ) -> JFSResult<()> {
        let data_size = (watches.len() + 1) * 255;
        let mut data = vec![0u8; data_size];
        let mut offset = 0;

        // First 255 bytes: watcher name
        let watcher_name = format!("{}/{}", config.pod_namespace, name);
        let name_bytes = watcher_name.as_bytes();
        let copy_len = std::cmp::min(name_bytes.len(), 255);
        data[offset..offset + copy_len].copy_from_slice(&name_bytes[..copy_len]);
        offset += 255;

        // Remaining 255-byte chunks: watch paths
        for watch in watches {
            let watch_bytes = watch.as_bytes();
            let copy_len = std::cmp::min(watch_bytes.len(), 255);
            data[offset..offset + copy_len].copy_from_slice(&watch_bytes[..copy_len]);
            offset += 255;
        }

        let msg = package_msg(msg_type, &data);

        write_tx
            .send(msg.to_vec())
            .map_err(|_| JFSError::ConnectionError("Failed to send watch message".to_string()))?;

        Ok(())
    }

    /**
        /// Start write handler
        fn start_write_handler(
            connection: &tokio::net::tcp::WriteHalf<'_>,
            write_tx: &broadcast::Sender<Vec<u8>>,
        ) {
            let connection: &tokio::net::tcp::WriteHalf<'_> = connection.clone();
            let mut write_rx = write_tx.subscribe();

            tokio::spawn(async move {
                while let Ok(data) = write_rx.recv().await {
                    // Create frame with length prefix
                    let mut frame = BytesMut::with_capacity(4 + data.len());

                    // Add 4-byte length field (BigEndian, data length only)
                    frame.put_u32(data.len() as u32);

                    // Add data
                    frame.put_slice(&data);

                    // Write the complete frame
                    let result = connection.write(&frame).await;
                    if let Err(e) = result {
                        log::error!("Write frame error: {}", e);
                    }
                }
            });
        }



        /// Start reading from connection
        fn start_reader(
            connection: &tokio::net::tcp::ReadHalf<'_>,
            events_tx: &Sender<Event>,
            errors_tx: &Sender<JFSError>,
            reconnect_tx: &Sender<()>,
        ) {
            let connection = Arc::clone(connection);
            let events_tx = events_tx.clone();
            let errors_tx = errors_tx.clone();
            let reconnect_tx = reconnect_tx.clone();

            tokio::spawn(async move {
                loop {
                    let mut conn_guard = connection.lock().await;
                    if let Some(conn) = conn_guard.as_mut() {
                        match conn.read_frame().await {
                            Ok(frame_data) => {
                                // Process the received frame data
                                if let Err(e) =
                                    Self::process_received_data(&frame_data, &events_tx, &errors_tx)
                                {
                                    let _ = errors_tx.try_send(e);
                                }
                            }
                            Err(e) => {
                                log::error!("Read frame error: {}", e);
                                let _ = errors_tx.try_send(e);
                                let _ = reconnect_tx.try_send(());
                                break;
                            }
                        }
                    } else {
                        break;
                    }
                }
            });
        }
    */
    /// Process received data from server
    fn process_received_data(
        data: &[u8],
        events_tx: &Sender<Event>,
        _errors_tx: &Sender<JFSError>,
    ) -> JFSResult<()> {
        let (msg_type, msg_data) = unpack_msg(data)?;

        match msg_type {
            MessageType::Event => {
                let events = unpack_events(msg_data)?;
                for event in events {
                    if let Err(_) = events_tx.try_send(event) {
                        // Channel is full, drop the event
                        log::warn!("Events channel is full, dropping event");
                    }
                }
            }
            _ => {
                log::debug!("Received message type: {:?}", msg_type);
            }
        }

        Ok(())
    }

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
            tokio::time::sleep(Duration::from_millis(10));
        }

        Ok(())
    }
}

impl JFSWatcher {
    /// Add a path to watch
    pub async fn add(&self, path: &str) -> JFSResult<()> {
        self.add_with(path, &[]).await
    }

    /// Add a path to watch with options
    pub async fn add_with(&self, path: &str, _opts: &[AddOption]) -> JFSResult<()> {
        // Add to local watch list
        {
            let mut watches = self.watches.write().await;
            watches.insert(path.to_string());
        }

        // Send to server
        Self::send_watch_message(
            &self.write_tx,
            &[path.to_string()],
            &self.name,
            &self.config,
            MessageType::Watch,
        )
        .await
    }

    /// Remove a path from watching
    pub async fn remove(&self, path: &str) -> JFSResult<()> {
        // Remove from local watch list
        {
            let mut watches = self.watches.write().await;
            watches.remove(path);
        }

        // Send to server
        Self::send_watch_message(
            &self.write_tx,
            &[path.to_string()],
            &self.name,
            &self.config,
            MessageType::Unwatch,
        )
        .await
    }

    /**
    /// Close the watcher
    pub async fn close(&self) -> JFSResult<()> {
        // Signal shutdown
        let _ = self.shutdown_tx.try_send(());

        // Close connection
        {
            let mut conn_guard = self.connection.lock().await;
            if let Some(mut conn) = conn_guard.take() {
                let _ = conn.shutdown().await;
            }
        }

        Ok(())
    }
    */
    /// Get the list of watched paths
    pub async fn watch_list(&self) -> JFSResult<Vec<String>> {
        let watches = self.watches.read().await;
        Ok(watches.iter().cloned().collect())
    }

    /// Get the events receiver
    pub fn events_receiver(&self) -> Receiver<Event> {
        self.events_rx.clone()
    }

    /// Get the errors receiver
    pub fn errors_receiver(&self) -> Receiver<JFSError> {
        self.errors_rx.clone()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    // Test imports removed as they're not used in current tests

    #[test]
    fn test_package_msg() {
        let mut data = vec![0u8; 255];
        let clear_name = format!("{}/{}", "search3monitor-q7qmz", "search3monitor");
        let name_bytes = clear_name.as_bytes();
        let copy_len = std::cmp::min(name_bytes.len(), 255);
        data[..copy_len].copy_from_slice(&name_bytes[..copy_len]);

        let msg = package_msg(MessageType::Clear, &data);

        // Create /data directory if it doesn't exist
        std::fs::create_dir_all("/data").expect("Failed to create /data directory");

        // Write msg to binary file
        std::fs::write("/data/rust.data", &msg).expect("Failed to write data to file");
    }
}
