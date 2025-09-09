use rust_jfs::{JFSWatcher, Op};
use std::time::Duration;
use tokio::time::sleep;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Initialize logging
    env_logger::init();

    // Create a new JFS watcher
    let watcher = JFSWatcher::new("example_watcher").await?;

    // Add some paths to watch
    watcher.add("/tmp/test_dir").await?;
    watcher.add("/tmp/another_dir").await?;

    // Get the event and error receivers
    let events_rx = watcher.events_receiver();
    let errors_rx = watcher.errors_receiver();

    println!("JFS Watcher started. Watching for file system events...");
    println!("Try creating, modifying, or deleting files in /tmp/test_dir or /tmp/another_dir");

    // Spawn a task to handle events
    let events_rx_clone = events_rx.clone();
    tokio::spawn(async move {
        while let Ok(event) = events_rx_clone.recv() {
            println!("Received event: {}", event);

            // Handle specific event types
            if event.has(Op::CREATE) {
                println!("  -> File created: {}", event.name);
            }
            if event.has(Op::WRITE) {
                println!("  -> File written: {}", event.name);
            }
            if event.has(Op::REMOVE) {
                println!("  -> File removed: {}", event.name);
            }
            if event.has(Op::RENAME) {
                println!("  -> File renamed: {}", event.name);
            }
        }
    });

    // Spawn a task to handle errors
    let errors_rx_clone = errors_rx.clone();
    tokio::spawn(async move {
        while let Ok(error) = errors_rx_clone.recv() {
            eprintln!("Watcher error: {}", error);
        }
    });

    // Run for a while
    sleep(Duration::from_secs(30)).await;

    // Get current watch list
    let watch_list = watcher.watch_list().await?;
    println!("Current watch list: {:?}", watch_list);

    // Remove a watch
    watcher.remove("/tmp/another_dir").await?;
    println!("Removed /tmp/another_dir from watch list");

    // Run for a bit more
    sleep(Duration::from_secs(10)).await;

    // Close the watcher
    watcher.close().await?;
    println!("Watcher closed");

    Ok(())
}
