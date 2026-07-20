// tastastas-embed — sidecar binary that embeds text using bge-small-en-v1.5
// (ONNX). Protocol: newline-delimited JSON on stdin/stdout.
//   in:  {"texts": ["a", "b"]}
//   out: {"embeddings": [[...], [...]]}
// Model + tokenizer are baked into the binary via include_bytes! so the
// released binary has zero external file dependencies at runtime.
use ort::session::{Session, builder::GraphOptimizationLevel};
use ort::value::Tensor;
use std::io::{self, BufRead, Write};
use tokenizers::Tokenizer;

const MODEL_BYTES: &[u8] = include_bytes!("../assets/model.onnx");
const TOKENIZER_BYTES: &[u8] = include_bytes!("../assets/tokenizer.json");

#[derive(serde::Deserialize)]
struct Request {
    texts: Vec<String>,
}

#[derive(serde::Serialize)]
struct Response {
    embeddings: Option<Vec<Vec<f32>>>,
    error: Option<String>,
}

fn mean_pool(hidden: &[f32], attn_mask: &[i64], seq_len: usize, hidden_size: usize) -> Vec<f32> {
    let mut sum = vec![0f32; hidden_size];
    let mut count = 0f32;
    for t in 0..seq_len {
        if attn_mask[t] == 0 {
            continue;
        }
        count += 1.0;
        for h in 0..hidden_size {
            sum[h] += hidden[t * hidden_size + h];
        }
    }
    if count > 0.0 {
        for v in sum.iter_mut() {
            *v /= count;
        }
    }
    let norm: f32 = sum.iter().map(|v| v * v).sum::<f32>().sqrt();
    if norm > 0.0 {
        for v in sum.iter_mut() {
            *v /= norm;
        }
    }
    sum
}

fn embed_batch(
    session: &mut Session,
    tokenizer: &Tokenizer,
    texts: &[String],
) -> Result<Vec<Vec<f32>>, String> {
    let encodings = tokenizer
        .encode_batch(texts.to_vec(), true)
        .map_err(|e| e.to_string())?;

    let max_len = encodings.iter().map(|e| e.len()).max().unwrap_or(0);
    let batch = encodings.len();

    let mut ids = vec![0i64; batch * max_len];
    let mut mask = vec![0i64; batch * max_len];
    let type_ids = vec![0i64; batch * max_len];

    for (i, enc) in encodings.iter().enumerate() {
        for (j, &id) in enc.get_ids().iter().enumerate() {
            ids[i * max_len + j] = id as i64;
        }
        for (j, &m) in enc.get_attention_mask().iter().enumerate() {
            mask[i * max_len + j] = m as i64;
        }
    }

    let input_ids = Tensor::from_array(([batch, max_len], ids.into_boxed_slice()))
        .map_err(|e| e.to_string())?;
    let attention_mask = Tensor::from_array(([batch, max_len], mask.clone().into_boxed_slice()))
        .map_err(|e| e.to_string())?;
    let token_type_ids = Tensor::from_array(([batch, max_len], type_ids.into_boxed_slice()))
        .map_err(|e| e.to_string())?;

    let outputs = session
        .run(ort::inputs![
            "input_ids" => input_ids,
            "attention_mask" => attention_mask,
            "token_type_ids" => token_type_ids,
        ])
        .map_err(|e| e.to_string())?;

    let (shape, data) = outputs[0]
        .try_extract_tensor::<f32>()
        .map_err(|e| e.to_string())?;
    let hidden_size = shape[2] as usize;

    let mut result = Vec::with_capacity(batch);
    for i in 0..batch {
        let start = i * max_len * hidden_size;
        let end = start + max_len * hidden_size;
        let attn = &mask[i * max_len..(i + 1) * max_len];
        result.push(mean_pool(&data[start..end], attn, max_len, hidden_size));
    }
    Ok(result)
}

fn main() {
    let tokenizer = Tokenizer::from_bytes(TOKENIZER_BYTES).expect("load tokenizer");

    ort::init().commit();
    let mut session = Session::builder()
        .expect("session builder")
        .with_optimization_level(GraphOptimizationLevel::Level3)
        .expect("opt level")
        .commit_from_memory(MODEL_BYTES)
        .expect("load model");

    let stdin = io::stdin();
    let stdout = io::stdout();
    for line in stdin.lock().lines() {
        let line = match line {
            Ok(l) => l,
            Err(_) => break,
        };
        if line.trim().is_empty() {
            continue;
        }

        let resp = match serde_json::from_str::<Request>(&line) {
            Ok(req) => match embed_batch(&mut session, &tokenizer, &req.texts) {
                Ok(embeddings) => Response { embeddings: Some(embeddings), error: None },
                Err(e) => Response { embeddings: None, error: Some(e) },
            },
            Err(e) => Response { embeddings: None, error: Some(format!("parse request: {e}")) },
        };

        let mut out = stdout.lock();
        let _ = serde_json::to_writer(&mut out, &resp);
        let _ = out.write_all(b"\n");
        let _ = out.flush();
    }
}
