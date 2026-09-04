//! Disposable wallet fixture for measuring the existing desktop sync engine.
//! Never use this with a real wallet database or a public lightwalletd endpoint.
use rust_lib_zcash_wallet::api::{sync, wallet};
use serde_json::json;
use std::{path::Path, time::Instant};
use zeroize::Zeroize;

fn run() -> Result<(), String> {
    let args: Vec<String> = std::env::args().collect();
    if args.len() != 5 {
        return Err(
            "usage: lwd_mainnet_wallet <create|sync> <db> <birthday|private-url> <1|2>".into(),
        );
    }
    let _ = rustls::crypto::ring::default_provider().install_default();
    let mode: u8 = args[4].parse().map_err(|_| "invalid sync mode")?;
    if mode != 1 && mode != 2 {
        return Err("mode must be 1 (foreground) or 2 (background)".into());
    }
    let started = Instant::now();
    match args[1].as_str() {
        "create" => {
            if Path::new(&args[2]).exists() {
                return Err("refusing to replace an existing database".into());
            }
            let birthday = args[3].parse().map_err(|_| "invalid birthday height")?;
            let mut created = wallet::create_wallet(
                "main".into(),
                args[2].clone(),
                Some(birthday),
                Some("Benchmark fixture".into()),
            )?;
            created.mnemonic.zeroize();
        }
        "sync" => {
            let endpoint = url::Url::parse(&args[3]).map_err(|e| e.to_string())?;
            // Runs through an SSH tunnel or a local recorder on the isolated host.
            if !matches!(
                endpoint.host_str(),
                Some("127.0.0.1") | Some("localhost") | Some("[::1]")
            ) {
                return Err("the fixture requires a loopback endpoint".into());
            }
            if !Path::new(&args[2]).is_file() {
                return Err("create a disposable database first".into());
            }
            sync::run_full_sync_blocking(args[2].clone(), args[3].clone(), "main".into(), mode)?;
            let status = sync::get_sync_status(args[2].clone(), "main".into())?;
            println!(
                "{}",
                json!({"scanned_height": status.scanned_height,
                "chain_tip_height": status.chain_tip_height, "is_complete": status.is_complete})
            );
            if !status.is_complete {
                return Err("sync returned without completing the wallet".into());
            }
        }
        _ => return Err("unknown command".into()),
    }
    println!(
        "{}",
        json!({"operation": args[1], "elapsed_seconds": started.elapsed().as_secs_f64(), "success": true})
    );
    Ok(())
}

fn main() {
    if let Err(error) = run() {
        eprintln!("{}", json!({"success": false, "error": error}));
        std::process::exit(1);
    }
}
