use rust_jfs::{common, JFSWatcher, Op};
use std::env;
use std::time::Duration;
use tokio::time::sleep;

// /data/pvc-userspace-harvey063-s2vmddryovcuqwnz/Home/Documents
// ./example  /data/pvc-userspace-harvey063-s2vmddryovcuqwnz/Home/Documents
// ps aux | grep rustexample | grep -v grep
// ./upload_to_mo.sh /var/wangzhong/fs-lib/rust-jfs/target/debug/rustexample
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Initialize logging
    common::init_logger();

    // Get command line arguments
    let args: Vec<String> = env::args().collect();
    let paths_to_watch = if args.len() >= 2 {
        // Use command line arguments if provided
        args[1..].to_vec()
    } else {
        // Try to get path from environment variable
        if let Ok(env_path) = env::var("WATCHED_PATH") {
            vec![env_path]
        } else {
            // Use default path
            vec!["/data/pvc-userspace-harvey063-s2vmddryovcuqwnz/Home/Documents".to_string()]
        }
    };

    // Create a new JFS watcher
    let watcher = JFSWatcher::new("mywatcher").await?;
    // watcher.start_connection_manager();

    // Add paths to watch
    for path in &paths_to_watch {
        log::info!("Adding watch for: {}", path);
        watcher.add(path).await?;
    }

    // Get the event and error receivers
    let events_rx = watcher.events_receiver();
    // let errors_rx = watcher.errors_receiver();

    log::info!("JFS Watcher started. Watching for file system events...");
    log::info!("Watching paths: {:?}", &paths_to_watch);
    log::info!("Try creating, modifying, or deleting files in the watched directories");

    // Spawn a task to handle events
    let events_rx_clone = events_rx.clone();
    tokio::spawn(async move {
        while let Ok(event) = events_rx_clone.recv() {
            log::info!("Received event: {}", event);

            // Handle specific event types
            if event.has(Op::CREATE) {
                log::info!("create event: {}", event);
            }
            if event.has(Op::WRITE) {
                log::info!("write event: {}", event);
            }
            if event.has(Op::REMOVE) {
                log::info!("remove event: {}", event);
            }
            if event.has(Op::RENAME) {
                log::info!("rename event: {}", event);
            }
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
        if paths_to_watch.len() > 1 {
            let path_to_remove = &paths_to_watch[paths_to_watch.len() - 1];
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
