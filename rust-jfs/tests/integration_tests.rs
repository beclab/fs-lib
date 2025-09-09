use rust_jfs::JFSWatcher;
use std::fs;
use tokio::time::{sleep, Duration};

#[tokio::test]
async fn test_basic_watching() -> Result<(), Box<dyn std::error::Error>> {
    // Create test directory
    let test_dir = "/tmp/rust_jfs_test";
    fs::create_dir_all(test_dir)?;

    // Create watcher
    let watcher = JFSWatcher::new("test_watcher").await?;
    let events_rx = watcher.events_receiver();
    let errors_rx = watcher.errors_receiver();

    // Add watch
    watcher.add(test_dir).await?;

    // Spawn event handler
    let events_rx_clone = events_rx.clone();
    let events_handle = tokio::spawn(async move {
        let mut events = Vec::new();
        let timeout = sleep(Duration::from_secs(5));
        tokio::pin!(timeout);

        loop {
            tokio::select! {
                event = async { events_rx_clone.recv() } => {
                    if let Ok(event) = event {
                        events.push(event);
                    } else {
                        break;
                    }
                }
                _ = &mut timeout => {
                    break;
                }
            }
        }
        events
    });

    // Spawn error handler
    let errors_rx_clone = errors_rx.clone();
    let errors_handle = tokio::spawn(async move {
        let mut errors = Vec::new();
        let timeout = sleep(Duration::from_secs(5));
        tokio::pin!(timeout);

        loop {
            tokio::select! {
                error = async { errors_rx_clone.recv() } => {
                    if let Ok(error) = error {
                        errors.push(error);
                    } else {
                        break;
                    }
                }
                _ = &mut timeout => {
                    break;
                }
            }
        }
        errors
    });

    // Create a test file
    let test_file = format!("{}/test_file.txt", test_dir);
    fs::write(&test_file, "Hello, World!")?;

    // Wait a bit for events
    sleep(Duration::from_millis(100)).await;

    // Modify the file
    fs::write(&test_file, "Hello, Rust!")?;

    // Wait a bit more
    sleep(Duration::from_millis(100)).await;

    // Delete the file
    fs::remove_file(&test_file)?;

    // Wait for handlers to finish
    let events = events_handle.await?;
    let errors = errors_handle.await?;

    // Clean up
    watcher.close().await?;
    fs::remove_dir_all(test_dir)?;

    // Check results
    println!("Received {} events", events.len());
    for event in &events {
        println!("  - {}", event);
    }

    if !errors.is_empty() {
        println!("Received {} errors", errors.len());
        for error in &errors {
            println!("  - {}", error);
        }
    }

    Ok(())
}

#[tokio::test]
async fn test_watch_list_management() -> Result<(), Box<dyn std::error::Error>> {
    let watcher = JFSWatcher::new("test_watcher_2").await?;

    // Initially empty
    let watch_list = watcher.watch_list().await?;
    assert!(watch_list.is_empty());

    // Add some watches
    watcher.add("/tmp/dir1").await?;
    watcher.add("/tmp/dir2").await?;
    watcher.add("/tmp/dir3").await?;

    // Check watch list
    let watch_list = watcher.watch_list().await?;
    assert_eq!(watch_list.len(), 3);
    assert!(watch_list.contains(&"/tmp/dir1".to_string()));
    assert!(watch_list.contains(&"/tmp/dir2".to_string()));
    assert!(watch_list.contains(&"/tmp/dir3".to_string()));

    // Remove one watch
    watcher.remove("/tmp/dir2").await?;

    // Check updated watch list
    let watch_list = watcher.watch_list().await?;
    assert_eq!(watch_list.len(), 2);
    assert!(watch_list.contains(&"/tmp/dir1".to_string()));
    assert!(!watch_list.contains(&"/tmp/dir2".to_string()));
    assert!(watch_list.contains(&"/tmp/dir3".to_string()));

    // Clean up
    watcher.close().await?;

    Ok(())
}

#[tokio::test]
async fn test_invalid_watcher_name() -> Result<(), Box<dyn std::error::Error>> {
    // This should fail because the name contains '/'
    let result = JFSWatcher::new("invalid/name").await;
    assert!(result.is_err());

    Ok(())
}
