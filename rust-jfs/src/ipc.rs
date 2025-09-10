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

/// Trait for frame-based connections that match Go's goframe protocol
#[async_trait::async_trait]
pub trait FrameConnection: Send + Sync {
    async fn read_frame(&mut self) -> crate::errors::JFSResult<Bytes>;
    async fn write_frame(&mut self, data: &[u8]) -> crate::errors::JFSResult<()>;
    async fn shutdown(&mut self) -> crate::errors::JFSResult<()>;
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

/// Frame-based connection wrapper that matches Go's goframe configuration
///
/// Go configuration:
/// - ByteOrder: binary.BigEndian
/// - LengthFieldLength: 4
/// - LengthAdjustment: 0
/// - LengthIncludesLengthFieldLength: false
/// - LengthFieldOffset: 0
/// - InitialBytesToStrip: 4
pub struct FrameConnectionWrapper<C: JFSConnection> {
    connection: C,
    read_buffer: BytesMut,
}

impl<C: JFSConnection> FrameConnectionWrapper<C> {
    pub fn new(connection: C) -> Self {
        FrameConnectionWrapper {
            connection,
            read_buffer: BytesMut::new(),
        }
    }
}

#[async_trait::async_trait]
impl<C: JFSConnection> FrameConnection for FrameConnectionWrapper<C> {
    /// Read a complete frame following Go's goframe protocol
    ///
    /// Protocol format:
    /// [4 bytes length][data...]
    /// - Length field is in BigEndian format
    /// - Length field does NOT include its own 4 bytes
    /// - We strip the 4-byte length field when returning data
    async fn read_frame(&mut self) -> crate::errors::JFSResult<Bytes> {
        loop {
            // Try to read a complete frame from buffer
            if self.read_buffer.len() >= 4 {
                // Read length field (4 bytes, BigEndian)
                let frame_length = u32::from_be_bytes([
                    self.read_buffer[0],
                    self.read_buffer[1],
                    self.read_buffer[2],
                    self.read_buffer[3],
                ]) as usize;

                // Check if we have the complete frame (4 bytes length + data)
                if self.read_buffer.len() >= 4 + frame_length {
                    // Split off the complete frame
                    let mut full_frame = self.read_buffer.split_to(4 + frame_length);
                    // Return only the data part (strip the 4-byte length field)
                    let data_part = full_frame.split_off(4);
                    return Ok(data_part.freeze());
                }
            }

            // Read more data from connection
            let mut buf = [0u8; 4096];
            let n = self
                .connection
                .read(&mut buf)
                .await
                .map_err(|e| crate::errors::JFSError::IoError(e))?;

            if n == 0 {
                return Err(crate::errors::JFSError::ConnectionError(
                    "Connection closed".to_string(),
                ));
            }

            self.read_buffer.put_slice(&buf[..n]);
        }
    }

    /// Write a complete frame following Go's goframe protocol
    ///
    /// Protocol format:
    /// [4 bytes length][data...]
    /// - Length field is in BigEndian format
    /// - Length field contains only the data length (not including the 4-byte length field)
    async fn write_frame(&mut self, data: &[u8]) -> crate::errors::JFSResult<()> {
        // Create frame with length prefix
        let mut frame = BytesMut::with_capacity(4 + data.len());

        // Add 4-byte length field (BigEndian, data length only)
        frame.put_u32(data.len() as u32);

        // Add data
        frame.put_slice(data);

        // Write the complete frame
        self.connection
            .write(&frame)
            .await
            .map_err(|e| crate::errors::JFSError::IoError(e))?;
        self.connection
            .flush()
            .await
            .map_err(|e| crate::errors::JFSError::IoError(e))?;
        Ok(())
    }

    /// Shutdown the connection
    async fn shutdown(&mut self) -> crate::errors::JFSResult<()> {
        self.connection
            .shutdown()
            .await
            .map_err(|e| crate::errors::JFSError::IoError(e))?;
        Ok(())
    }
}
