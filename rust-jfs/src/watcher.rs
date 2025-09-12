use crate::errors::JFSError;
use crate::event::Event;
use crate::utils::{
    connect_and_run, get_env_var, package_msg, parse_dial_target, unpack_events, unpack_msg,
    MessageType,
};
use anyhow::Result;
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
    async fn add(&self, path: &str) -> Result<()>;
    async fn add_with(&self, path: &str, _opts: &[AddOption]) -> Result<()>;
    async fn remove(&self, path: &str) -> Result<()>;
    async fn close(&self) -> Result<()>;
    async fn watch_list(&self) -> Result<Vec<String>>;

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
    write_tx: Sender<Vec<u8>>,
    write_rx: Receiver<Vec<u8>>,
}

impl JFSWatcher {
    /// Create a new JFS watcher
    pub async fn new(name: &str) -> Result<Self> {
        log::info!("Creating new JFS watcher for {}", &name);
        Self::with_config(name, JFSConfig::default()).await
    }

    /// Create a new JFS watcher with custom config
    pub async fn new_with_config(name: &str, config: JFSConfig) -> Result<Self> {
        Self::with_config(name, config).await
    }

    async fn with_config(name: &str, config: JFSConfig) -> Result<Self> {
        if name.contains('/') {
            return Err(anyhow::Error::from(JFSError::BadWatcherName));
        }

        let (events_tx, events_rx) = bounded(1000);
        let (write_tx, write_rx) = bounded(100);

        let watcher_name = format!("{}/{}/{}", config.pod_name, config.container_name, name);

        let watcher = JFSWatcher {
            config,
            name: watcher_name,
            watches: Arc::new(RwLock::new(HashSet::new())),
            events_tx,
            events_rx,
            write_rx,
            write_tx,
        };

        log::info!("Starting connection manager for {}", &name);
        // Start the connection manager
        watcher.start_connection_manager().await;

        Ok(watcher)
    }

    /// Start the connection manager task
    pub async fn start_connection_manager(&self) {
        let config = self.config.clone();
        let name = self.name.clone();
        let watches: Arc<RwLock<HashSet<String>>> = Arc::clone(&self.watches);
        let write_rx: Receiver<Vec<u8>> = self.write_rx.clone();
        let write_tx: Sender<Vec<u8>> = self.write_tx.clone();
        let events_tx: Sender<Event> = self.events_tx.clone();
        JFSWatcher::send_clear_signal(&write_tx, &name, &config)
            .await
            .unwrap();

        // 将重连循环放到独立线程中，让主线程非阻塞
        let reconnect_handle = tokio::spawn(async move {
            let mut retry_count = 0;
            const MAX_RETRIES: u32 = 1000000;

            loop {
                log::info!("Attempting to connect (attempt {})...", retry_count + 1);

                match connect_and_run(
                    write_rx.clone(),
                    write_tx.clone(),
                    events_tx.clone(),
                    &config.server_addr,
                    &watches,
                    &name,
                    &config,
                )
                .await
                {
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
    }

    /// Send clear signal to server
    async fn send_clear_signal(
        write_tx: &Sender<Vec<u8>>,
        name: &str,
        config: &JFSConfig,
    ) -> Result<()> {
        let mut data = vec![0u8; 255];
        let clear_name = format!("{}/{}", config.pod_namespace, name);
        let name_bytes = clear_name.as_bytes();
        let copy_len = std::cmp::min(name_bytes.len(), 255);
        data[..copy_len].copy_from_slice(&name_bytes[..copy_len]);

        let msg = package_msg(MessageType::Clear, &data);

        write_tx.send(msg.to_vec()).map_err(|_| {
            anyhow::Error::from(JFSError::ConnectionError(
                "Failed to send clear signal".to_string(),
            ))
        })?;

        Ok(())
    }

    /// Resend all watches to server
    pub async fn resend_watches(
        write_tx: &Sender<Vec<u8>>,
        watches: &Arc<RwLock<HashSet<String>>>,
        name: &str,
        config: &JFSConfig,
    ) -> Result<()> {
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
    ) -> Result<()> {
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

        write_tx.send(msg.to_vec()).map_err(|_| {
            anyhow::Error::from(JFSError::ConnectionError(
                "Failed to send watch message".to_string(),
            ))
        })?;

        Ok(())
    }

    /// Process received data from server
    fn process_received_data(data: &[u8], events_tx: &Sender<Event>) -> Result<()> {
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
}

impl JFSWatcher {
    /// Add a path to watch
    pub async fn add(&self, path: &str) -> Result<()> {
        self.add_with(path, &[]).await
    }

    /// Add a path to watch with options
    pub async fn add_with(&self, path: &str, _opts: &[AddOption]) -> Result<()> {
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
    pub async fn remove(&self, path: &str) -> Result<()> {
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
    pub async fn watch_list(&self) -> Result<Vec<String>> {
        let watches = self.watches.read().await;
        Ok(watches.iter().cloned().collect())
    }

    /// Get the events receiver
    pub fn events_receiver(&self) -> Receiver<Event> {
        self.events_rx.clone()
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
