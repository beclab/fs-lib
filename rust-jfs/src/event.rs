use std::fmt;

/// File operation that triggered the event.
/// This is a bitmask and some systems may send multiple operations at once.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Op(u32);

impl Op {
    /// A new pathname was created.
    pub const CREATE: Op = Op(1 << 0);

    /// The pathname was written to; this does *not* mean the write has finished,
    /// and a write can be followed by more writes.
    pub const WRITE: Op = Op(1 << 1);

    /// The path was removed; any watches on it will be removed. Some "remove"
    /// operations may trigger a Rename if the file is actually moved (for
    /// example "remove to trash" is often a rename).
    pub const REMOVE: Op = Op(1 << 2);

    /// The path was renamed to something else; any watched on it will be
    /// removed.
    pub const RENAME: Op = Op(1 << 3);

    /// File attributes were changed.
    /// It's generally not recommended to take action on this event, as it may
    /// get triggered very frequently by some software. For example, Spotlight
    /// indexing on macOS, anti-virus software, backup software, etc.
    pub const CHMOD: Op = Op(1 << 4);

    /// Check if this operation has the given operation.
    pub fn has(&self, other: Op) -> bool {
        self.0 & other.0 == other.0
    }

    /// Get the raw u32 value
    pub fn as_u32(&self) -> u32 {
        self.0
    }

    /// Create from u32 value
    pub fn from_u32(value: u32) -> Self {
        Op(value)
    }
}

impl fmt::Display for Op {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut parts = Vec::new();

        if self.has(Op::CREATE) {
            parts.push("CREATE");
        }
        if self.has(Op::REMOVE) {
            parts.push("REMOVE");
        }
        if self.has(Op::WRITE) {
            parts.push("WRITE");
        }
        if self.has(Op::RENAME) {
            parts.push("RENAME");
        }
        if self.has(Op::CHMOD) {
            parts.push("CHMOD");
        }

        if parts.is_empty() {
            write!(f, "[no events]")
        } else {
            write!(f, "{}", parts.join("|"))
        }
    }
}

/// Event represents a file system notification.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Event {
    /// Path to the file or directory.
    /// Paths are relative to the input; for example with Add("dir") the Name
    /// will be set to "dir/file" if you create that file, but if you use
    /// Add("/path/to/dir") it will be "/path/to/dir/file".
    pub name: String,

    /// File operation that triggered the event.
    /// This is a bitmask and some systems may send multiple operations at once.
    /// Use the Event.has() method instead of comparing with ==.
    pub op: Op,

    /// Key for the event
    pub key: String,
}

impl Event {
    /// Create a new event
    pub fn new(name: String, op: Op, key: String) -> Self {
        Event { name, op, key }
    }

    /// Check if this event has the given operation.
    pub fn has(&self, op: Op) -> bool {
        self.op.has(op)
    }
}

impl fmt::Display for Event {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{:<13} {:?}", self.op, self.name)
    }
}
