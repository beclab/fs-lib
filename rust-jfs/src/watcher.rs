use crate::errors::{JFSError, JFSResult};
use crate::event::Event;
use crate::ipc::{IpcConnection, JFSConnection, TcpConnection};
use crate::utils::{
    get_env_var, package_msg, parse_dial_target, unpack_events, unpack_msg, MessageType,
};
use async_trait::async_trait;
use crossbeam_channel::{bounded, Receiver, Sender};
use std::collections::HashSet;
use std::sync::Arc;
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
    connection: Arc<Mutex<Option<Box<dyn JFSConnection>>>>,
    shutdown_tx: Sender<()>,
    shutdown_rx: Receiver<()>,
    reconnect_tx: Sender<()>,
    reconnect_rx: Receiver<()>,
}

impl JFSWatcher {
    /// Create a new JFS watcher
    pub async fn new(name: &str) -> JFSResult<Self> {
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

        let watcher_name = format!("{}/{}/{}", config.pod_name, config.container_name, name);

        let watcher = JFSWatcher {
            config,
            name: watcher_name,
            watches: Arc::new(RwLock::new(HashSet::new())),
            events_tx,
            events_rx,
            errors_tx,
            errors_rx,
            connection: Arc::new(Mutex::new(None)),
            shutdown_tx,
            shutdown_rx,
            reconnect_tx,
            reconnect_rx,
        };

        // Start the connection manager
        watcher.start_connection_manager();

        Ok(watcher)
    }

    /// Start the connection manager task
    fn start_connection_manager(&self) {
        let config = self.config.clone();
        let name = self.name.clone();
        let watches = Arc::clone(&self.watches);
        let connection = Arc::clone(&self.connection);
        let events_tx = self.events_tx.clone();
        let errors_tx = self.errors_tx.clone();
        let shutdown_rx = self.shutdown_rx.clone();
        let reconnect_rx = self.reconnect_rx.clone();
        let reconnect_tx = self.reconnect_tx.clone();

        tokio::spawn(async move {
            let reconnect_rx = reconnect_rx;

            loop {
                tokio::select! {
                    _ = async { shutdown_rx.recv() } => {
                        log::info!("Connection manager shutting down");
                        break;
                    }
                    _ = async { reconnect_rx.recv() } => {
                        log::info!("Reconnection requested");
                    }
                }

                // Try to connect
                match Self::connect_to_server(&config, &name).await {
                    Ok(conn) => {
                        log::info!("Successfully connected to {}", config.server_addr);

                        // Store the connection
                        {
                            let mut conn_guard = connection.lock().await;
                            *conn_guard = Some(conn);
                        }

                        // Send clear signal
                        if let Err(e) = Self::send_clear_signal(&connection, &name, &config).await {
                            log::error!("Failed to send clear signal: {}", e);
                        }

                        // Re-send all watches
                        if let Err(e) =
                            Self::resend_watches(&connection, &watches, &name, &config).await
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
            }
        });
    }

    /// Connect to the JFS server
    async fn connect_to_server(
        config: &JFSConfig,
        _name: &str,
    ) -> JFSResult<Box<dyn JFSConnection>> {
        let (protocol, addr) = parse_dial_target(&config.server_addr)?;

        match protocol.as_str() {
            "ipc" => {
                let conn = IpcConnection::connect(&addr).await?;
                Ok(Box::new(conn))
            }
            "tcp" | "unix" => {
                let conn = TcpConnection::connect(&addr).await?;
                Ok(Box::new(conn))
            }
            _ => Err(JFSError::ConnectionError(format!(
                "Unsupported protocol: {}",
                protocol
            ))),
        }
    }

    /// Send clear signal to server
    async fn send_clear_signal(
        connection: &Arc<Mutex<Option<Box<dyn JFSConnection>>>>,
        name: &str,
        config: &JFSConfig,
    ) -> JFSResult<()> {
        let mut data = vec![0u8; 255];
        let clear_name = format!("{}/{}", config.pod_namespace, name);
        let name_bytes = clear_name.as_bytes();
        let copy_len = std::cmp::min(name_bytes.len(), 255);
        data[..copy_len].copy_from_slice(&name_bytes[..copy_len]);

        let msg = package_msg(MessageType::Clear, &data);

        let mut conn_guard = connection.lock().await;
        if let Some(conn) = conn_guard.as_mut() {
            conn.write(&msg).await.map_err(|e| JFSError::IoError(e))?;
            conn.flush().await.map_err(|e| JFSError::IoError(e))?;
        }

        Ok(())
    }

    /// Resend all watches to server
    async fn resend_watches(
        connection: &Arc<Mutex<Option<Box<dyn JFSConnection>>>>,
        watches: &Arc<RwLock<HashSet<String>>>,
        name: &str,
        config: &JFSConfig,
    ) -> JFSResult<()> {
        let watch_list = watches.read().await;
        if watch_list.is_empty() {
            return Ok(());
        }

        Self::send_watch_message(
            connection,
            &watch_list.iter().cloned().collect::<Vec<_>>(),
            name,
            config,
            MessageType::Watch,
        )
        .await
    }

    /// Send watch/unwatch message to server
    async fn send_watch_message(
        connection: &Arc<Mutex<Option<Box<dyn JFSConnection>>>>,
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

        let mut conn_guard = connection.lock().await;
        if let Some(conn) = conn_guard.as_mut() {
            conn.write(&msg).await.map_err(|e| JFSError::IoError(e))?;
            conn.flush().await.map_err(|e| JFSError::IoError(e))?;
        }

        Ok(())
    }

    /// Start reading from connection
    fn start_reader(
        connection: &Arc<Mutex<Option<Box<dyn JFSConnection>>>>,
        events_tx: &Sender<Event>,
        errors_tx: &Sender<JFSError>,
        reconnect_tx: &Sender<()>,
    ) {
        let connection = Arc::clone(connection);
        let events_tx = events_tx.clone();
        let errors_tx = errors_tx.clone();
        let reconnect_tx = reconnect_tx.clone();

        tokio::spawn(async move {
            let mut buffer = vec![0u8; 4096];

            loop {
                let mut conn_guard = connection.lock().await;
                if let Some(conn) = conn_guard.as_mut() {
                    match conn.read(&mut buffer).await {
                        Ok(0) => {
                            log::info!("Connection closed by server");
                            let _ = reconnect_tx.try_send(());
                            break;
                        }
                        Ok(n) => {
                            // Process the received data
                            if let Err(e) =
                                Self::process_received_data(&buffer[..n], &events_tx, &errors_tx)
                            {
                                let _ = errors_tx.try_send(e);
                            }
                        }
                        Err(e) => {
                            log::error!("Read error: {}", e);
                            let _ = errors_tx.try_send(JFSError::IoError(e));
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
            &self.connection,
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
            &self.connection,
            &[path.to_string()],
            &self.name,
            &self.config,
            MessageType::Unwatch,
        )
        .await
    }

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
