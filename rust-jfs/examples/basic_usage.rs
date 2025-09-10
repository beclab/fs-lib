use rust_jfs::{common, JFSWatcher, Op};
use std::env;
use std::time::Duration;
use tokio::time::sleep;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Initialize logging
    common::init_logger();

    // Get command line arguments
    let args: Vec<String> = env::args().collect();
    if args.len() < 2 {
        log::error!("Usage: {} <path1> [path2] [path3] ...", args[0]);
        log::error!("Example: {} /tmp/test_dir /tmp/another_dir", args[0]);
        std::process::exit(1);
    }

    // Create a new JFS watcher
    let watcher = JFSWatcher::new("example_watcher").await?;
    // watcher.start_connection_manager();

    // Add paths from command line arguments
    for path in &args[1..] {
        log::info!("Adding watch for: {}", path);
        watcher.add(path).await?;
    }

    // Get the event and error receivers
    let events_rx = watcher.events_receiver();
    let errors_rx = watcher.errors_receiver();

    log::info!("JFS Watcher started. Watching for file system events...");
    log::info!("Watching paths: {:?}", &args[1..]);
    log::info!("Try creating, modifying, or deleting files in the watched directories");

    // Spawn a task to handle events
    let events_rx_clone = events_rx.clone();
    tokio::spawn(async move {
        while let Ok(event) = events_rx_clone.recv() {
            log::info!("Received event: {}", event);

            // Handle specific event types
            if event.has(Op::CREATE) {
                log::info!("  -> File created: {}", event.name);
            }
            if event.has(Op::WRITE) {
                log::info!("  -> File written: {}", event.name);
            }
            if event.has(Op::REMOVE) {
                log::info!("  -> File removed: {}", event.name);
            }
            if event.has(Op::RENAME) {
                log::info!("  -> File renamed: {}", event.name);
            }
        }
    });

    // Spawn a task to handle errors
    let errors_rx_clone = errors_rx.clone();
    tokio::spawn(async move {
        while let Ok(error) = errors_rx_clone.recv() {
            log::error!("Watcher error: {}", error);
        }
    });

    loop {
        sleep(Duration::from_secs(10)).await;
        log::info!(
            "Sleeping for 10 seconds current time is {}",
            chrono::Utc::now()
        );
    }

    /* *
        // Run for a while
        sleep(Duration::from_secs(30)).await;

        // Get current watch list
        let watch_list = watcher.watch_list().await?;
        println!("Current watch list: {:?}", watch_list);

        // Remove a watch (remove the last path if there are multiple)
        if args.len() > 2 {
            let path_to_remove = &args[args.len() - 1];
            watcher.remove(path_to_remove).await?;
            println!("Removed {} from watch list", path_to_remove);
        }

        // Run for a bit more
        sleep(Duration::from_secs(10)).await;

        // Close the watcher
        watcher.close().await?;
        println!("Watcher closed");
    */
    Ok(())
}
