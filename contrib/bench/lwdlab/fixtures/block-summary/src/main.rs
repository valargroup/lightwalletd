//! Preparation-only block metadata from the pinned wallet transaction library.
//! Reads public raw blocks; never accesses a wallet or submits transactions.
use serde_json::json;
use std::io::{self, BufRead, Write};
use zcash_primitives::block::Block;
use zcash_protocol::consensus::MAIN_NETWORK;

fn summarize(line: &str) -> Result<serde_json::Value, String> {
    let input: serde_json::Value = serde_json::from_str(line).map_err(|e| e.to_string())?;
    let height = input["height"].as_u64().ok_or("missing height")?;
    if height == 0 || height > u32::MAX as u64 { return Err("height outside supported range".into()); }
    let bytes = hex::decode(input["hex"].as_str().ok_or("missing block hex")?).map_err(|e| e.to_string())?;
    let mut remaining = bytes.as_slice();
    let block = Block::read(&mut remaining, &MAIN_NETWORK).map_err(|e| e.to_string())?;
    if !remaining.is_empty() || u32::from(block.claimed_height()) as u64 != height {
        return Err("block height or trailing bytes mismatch".into());
    }
    let txids: Vec<String> = block.vtx().iter().map(|t| t.txid().to_string()).collect();
    let sapling: usize = block.vtx().iter().filter_map(|t| t.sapling_bundle()).map(|b| b.shielded_outputs().len()).sum();
    let orchard: usize = block.vtx().iter().filter_map(|t| t.orchard_bundle()).map(|b| b.actions().len()).sum();
    let ironwood: usize = block.vtx().iter().filter_map(|t| t.ironwood_bundle()).map(|b| b.actions().len()).sum();
    Ok(json!({"height":height,"hash":block.header().hash().to_string(),"tx":txids,"commitments_added":{"sapling":sapling,"orchard":orchard,"ironwood":ironwood}}))
}

fn main() {
    let mut out = io::BufWriter::new(io::stdout().lock());
    for line in io::stdin().lock().lines() {
        let result = line.map_err(|e| e.to_string()).and_then(|s| summarize(&s));
        match result {
            Ok(value) => { writeln!(out, "{value}").unwrap(); out.flush().unwrap(); }
            Err(error) => { eprintln!("{}", json!({"error":error})); std::process::exit(1); }
        }
    }
}
