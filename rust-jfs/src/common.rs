use chrono::Local;
use log::info;
use std::io::Write;
use std::sync::Once;

static INIT: Once = Once::new();

pub fn init_logger() {
    INIT.call_once(|| {
        let env: env_logger::Env<'_> =
            env_logger::Env::default().filter_or(env_logger::DEFAULT_FILTER_ENV, "info");
        env_logger::Builder::from_env(env)
            .format(|buf, record| {
                writeln!(
                    buf,
                    "{}:{} {}  {} [{}] {}",
                    record.file().unwrap_or("unknown"),
                    record.line().unwrap_or(0),
                    Local::now().format("%Y-%m-%d %H:%M:%S"),
                    record.level(),
                    record.module_path().unwrap_or("<unnamed>"),
                    &record.args()
                )
            })
            .init();
        std::env::set_var("RUST_LOG", "debug");

        info!("env_logger initialized.");
    });
}
