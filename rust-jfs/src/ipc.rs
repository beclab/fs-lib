use crate::errors::{JFSError, JFSResult};
use bytes::{BufMut, Bytes, BytesMut};
use std::io;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream as AsyncTcpStream;

/// Trait for connection types that can be used with JFS
#[async_trait::async_trait]
pub trait JFSConnection: Send + Sync {
    async fn read(&mut self, buf: &mut [u8]) -> io::Result<usize>;
    async fn write(&mut self, buf: &[u8]) -> io::Result<usize>;
    async fn flush(&mut self) -> io::Result<()>;
    async fn shutdown(&mut self) -> io::Result<()>;
}

/// TCP connection implementation
pub struct TcpConnection {
    stream: AsyncTcpStream,
}

impl TcpConnection {
    pub async fn connect(addr: &str) -> JFSResult<Self> {
        let stream = AsyncTcpStream::connect(addr)
            .await
            .map_err(|e| JFSError::ConnectionError(format!("Failed to connect: {}", e)))?;

        Ok(TcpConnection { stream })
    }
}

#[async_trait::async_trait]
impl JFSConnection for TcpConnection {
    async fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        self.stream.read(buf).await
    }

    async fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.stream.write(buf).await
    }

    async fn flush(&mut self) -> io::Result<()> {
        self.stream.flush().await
    }

    async fn shutdown(&mut self) -> io::Result<()> {
        self.stream.shutdown().await
    }
}

/// IPC connection implementation using named pipes
pub struct IpcConnection {
    // Simplified implementation - just use TCP for now
    tcp_conn: TcpConnection,
}

impl IpcConnection {
    pub async fn connect(name: &str) -> JFSResult<Self> {
        // For now, treat IPC as TCP connection
        // In a real implementation, you would use actual IPC mechanisms
        let tcp_conn = TcpConnection::connect(name).await?;
        Ok(IpcConnection { tcp_conn })
    }
}

#[async_trait::async_trait]
impl JFSConnection for IpcConnection {
    async fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        self.tcp_conn.read(buf).await
    }

    async fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.tcp_conn.write(buf).await
    }

    async fn flush(&mut self) -> io::Result<()> {
        self.tcp_conn.flush().await
    }

    async fn shutdown(&mut self) -> io::Result<()> {
        self.tcp_conn.shutdown().await
    }
}

/// Frame-based connection wrapper
pub struct FrameConnection<C: JFSConnection> {
    connection: C,
    read_buffer: BytesMut,
}

impl<C: JFSConnection> FrameConnection<C> {
    pub fn new(connection: C) -> Self {
        FrameConnection {
            connection,
            read_buffer: BytesMut::new(),
        }
    }

    /// Read a complete frame
    pub async fn read_frame(&mut self) -> JFSResult<Bytes> {
        loop {
            // Try to read a complete frame from buffer
            if self.read_buffer.len() >= 4 {
                let frame_length = u32::from_be_bytes([
                    self.read_buffer[0],
                    self.read_buffer[1],
                    self.read_buffer[2],
                    self.read_buffer[3],
                ]) as usize;

                if self.read_buffer.len() >= 4 + frame_length {
                    let frame = self.read_buffer.split_to(4 + frame_length);
                    return Ok(frame.freeze());
                }
            }

            // Read more data
            let mut buf = [0u8; 4096];
            let n = self
                .connection
                .read(&mut buf)
                .await
                .map_err(|e| JFSError::IoError(e))?;

            if n == 0 {
                return Err(JFSError::ConnectionError("Connection closed".to_string()));
            }

            self.read_buffer.put_slice(&buf[..n]);
        }
    }

    /// Write a complete frame
    pub async fn write_frame(&mut self, data: &[u8]) -> JFSResult<()> {
        self.connection
            .write(data)
            .await
            .map_err(|e| JFSError::IoError(e))?;
        self.connection
            .flush()
            .await
            .map_err(|e| JFSError::IoError(e))?;
        Ok(())
    }

    /// Shutdown the connection
    pub async fn shutdown(&mut self) -> JFSResult<()> {
        self.connection
            .shutdown()
            .await
            .map_err(|e| JFSError::IoError(e))?;
        Ok(())
    }
}
