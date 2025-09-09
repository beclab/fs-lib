use crate::errors::{JFSError, JFSResult};
use crate::event::{Event, Op};
use bytes::{BufMut, Bytes, BytesMut};
use url::Url;

/// Message types for the JFS protocol
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MessageType {
    Error = 0,
    Watch = 1,
    Unwatch = 2,
    Clear = 3,
    Suspend = 4,
    Resume = 5,
    Event = 6,
}

impl MessageType {
    pub fn as_u32(&self) -> u32 {
        *self as u32
    }

    pub fn from_u32(value: u32) -> JFSResult<Self> {
        match value {
            0 => Ok(MessageType::Error),
            1 => Ok(MessageType::Watch),
            2 => Ok(MessageType::Unwatch),
            3 => Ok(MessageType::Clear),
            4 => Ok(MessageType::Suspend),
            5 => Ok(MessageType::Resume),
            6 => Ok(MessageType::Event),
            _ => Err(JFSError::InvalidMessage(format!(
                "Unknown message type: {}",
                value
            ))),
        }
    }
}

/// Frame size for JFS protocol
pub const FRAME_SIZE: usize = 255 + 4 + 255; // path(255) + op(4) + key(255)

/// Package a message with type and data
pub fn package_msg(msg_type: MessageType, data: &[u8]) -> Bytes {
    let mut buf = BytesMut::with_capacity(4 + data.len());
    buf.put_u32(msg_type.as_u32());
    buf.put_slice(data);
    buf.freeze()
}

/// Unpack a message to get type and data
pub fn unpack_msg(data: &[u8]) -> JFSResult<(MessageType, &[u8])> {
    if data.len() < 4 {
        return Err(JFSError::InvalidMessage("Message too short".to_string()));
    }

    let msg_type = MessageType::from_u32(u32::from_be_bytes([data[0], data[1], data[2], data[3]]))?;
    let msg_data = if data.len() > 4 { &data[4..] } else { &[] };

    Ok((msg_type, msg_data))
}

/// Pack an event into bytes
pub fn pack_event(event: &Event) -> Bytes {
    let mut data = BytesMut::with_capacity(FRAME_SIZE);

    // Path (255 bytes, padded with nulls)
    let mut path_bytes = [0u8; 255];
    let path_slice = event.name.as_bytes();
    let copy_len = std::cmp::min(path_slice.len(), 255);
    path_bytes[..copy_len].copy_from_slice(&path_slice[..copy_len]);
    data.put_slice(&path_bytes);

    // Operation (4 bytes)
    data.put_u32(event.op.as_u32());

    // Key (255 bytes, padded with nulls)
    let mut key_bytes = [0u8; 255];
    let key_slice = event.key.as_bytes();
    let copy_len = std::cmp::min(key_slice.len(), 255);
    key_bytes[..copy_len].copy_from_slice(&key_slice[..copy_len]);
    data.put_slice(&key_bytes);

    data.freeze()
}

/// Unpack events from bytes
pub fn unpack_events(event_data: &[u8]) -> JFSResult<Vec<Event>> {
    if event_data.len() < FRAME_SIZE {
        return Err(JFSError::InvalidEvent("Event data too short".to_string()));
    }

    let count = event_data.len() / FRAME_SIZE;
    let mut events = Vec::with_capacity(count);

    for i in 0..count {
        let offset = i * FRAME_SIZE;
        if offset + FRAME_SIZE > event_data.len() {
            break;
        }

        let frame = &event_data[offset..offset + FRAME_SIZE];

        // Extract path (first 255 bytes, trim nulls)
        let path_bytes = &frame[0..255];
        let path = String::from_utf8_lossy(path_bytes)
            .trim_end_matches('\0')
            .to_string();

        // Extract operation (next 4 bytes)
        let op_bytes = &frame[255..259];
        let op = Op::from_u32(u32::from_be_bytes([
            op_bytes[0],
            op_bytes[1],
            op_bytes[2],
            op_bytes[3],
        ]));

        // Extract key (last 255 bytes, trim nulls)
        let key_bytes = &frame[259..514];
        let key = String::from_utf8_lossy(key_bytes)
            .trim_end_matches('\0')
            .to_string();

        events.push(Event::new(path, op, key));
    }

    Ok(events)
}

/// Parse dial target to get network type and address
pub fn parse_dial_target(target: &str) -> JFSResult<(String, String)> {
    // Check for unix:addr format
    if let Some(colon_pos) = target.find(':') {
        if !target.contains("://") {
            if &target[..colon_pos] == "unix" {
                return Ok(("unix".to_string(), target[colon_pos + 1..].to_string()));
            }
        }
    }

    // Try to parse as URL
    if target.contains("://") {
        let url = Url::parse(target)
            .map_err(|e| JFSError::InvalidMessage(format!("Invalid URL: {}", e)))?;

        let scheme = url.scheme();
        let addr = if scheme == "unix" || scheme == "ipc" {
            if url.path().is_empty() {
                url.host_str().unwrap_or("").to_string()
            } else {
                url.path().to_string()
            }
        } else {
            target.to_string()
        };

        return Ok((scheme.to_string(), addr));
    }

    // Default to TCP
    Ok(("tcp".to_string(), target.to_string()))
}

/// Check if an error is a socket error
pub fn is_socket_error(err: &std::io::Error) -> bool {
    use std::io::ErrorKind;
    matches!(
        err.kind(),
        ErrorKind::ConnectionRefused
            | ErrorKind::ConnectionReset
            | ErrorKind::ConnectionAborted
            | ErrorKind::NotConnected
            | ErrorKind::BrokenPipe
    )
}

/// Get environment variable with fallback
pub fn get_env_var(var: &str, fallbacks: &[&str]) -> String {
    if let Ok(value) = std::env::var(var) {
        return value;
    }

    for fallback in fallbacks {
        if let Ok(value) = std::env::var(fallback) {
            return value;
        }
    }

    "default".to_string()
}
