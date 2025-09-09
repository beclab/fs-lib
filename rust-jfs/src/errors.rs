use thiserror::Error;

#[derive(Error, Debug)]
pub enum JFSError {
    #[error("fsnotify: can't remove non-existent watcher")]
    NonExistentWatch,

    #[error("fsnotify: queue or buffer overflow")]
    EventOverflow,

    #[error("fsnotify: watcher already closed")]
    Closed,

    #[error("bad watcher name, illegal character '/'")]
    BadWatcherName,

    #[error("unknown fs type: {0}")]
    UnknownFSType(String),

    #[error("connection error: {0}")]
    ConnectionError(String),

    #[error("invalid message: {0}")]
    InvalidMessage(String),

    #[error("invalid event: {0}")]
    InvalidEvent(String),

    #[error("io error: {0}")]
    IoError(#[from] std::io::Error),

    #[error("ipc error: {0}")]
    IpcError(#[from] ipc_channel::Error),
}

pub type JFSResult<T> = Result<T, JFSError>;
