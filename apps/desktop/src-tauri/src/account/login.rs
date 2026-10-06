//! 浏览器登录（RFC 8252）：系统浏览器打开登录页 → 用户输入邮箱验证码 → 跳回本机回环地址 →
//! 用授权码 + PKCE verifier 换取令牌。登录页在用户自己的浏览器里，Google / Apple 登录以后也走同样的流程。

use std::{
    io::{BufRead, BufReader, Write},
    net::{TcpListener, TcpStream},
    time::{Duration, Instant},
};

use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};
use serde_json::json;
use sha2::{Digest, Sha256};

use super::{AuthError, Credentials, TokenResponse};

/// 等待用户在浏览器里完成登录的最长时间
pub const LOGIN_TIMEOUT: Duration = Duration::from_secs(5 * 60);

fn random_b64url(bytes: usize) -> String {
    // uuid v4 的随机数来自系统 CSPRNG
    let mut buf = Vec::with_capacity(bytes + 16);
    while buf.len() < bytes {
        buf.extend_from_slice(uuid::Uuid::new_v4().as_bytes());
    }
    buf.truncate(bytes);
    URL_SAFE_NO_PAD.encode(buf)
}

pub struct LoginRequest {
    pub url: String,
    listener: TcpListener,
    state: String,
    verifier: String,
}

/// 准备登录：开本机回调端口，生成 PKCE 参数和登录页 URL。
pub fn prepare(creds: &Credentials) -> Result<LoginRequest, AuthError> {
    let listener = TcpListener::bind("127.0.0.1:0").map_err(|e| AuthError::Server(format!("无法开启本机回调端口：{e}")))?;
    let port = listener.local_addr().map_err(|e| AuthError::Server(e.to_string()))?.port();
    let verifier = random_b64url(32);
    let challenge = URL_SAFE_NO_PAD.encode(Sha256::digest(verifier.as_bytes()));
    let state = random_b64url(16);
    let url = reqwest::Url::parse_with_params(
        &creds.url("/auth/login"),
        &[
            ("device_id", creds.device_id.as_str()),
            ("redirect_uri", &format!("http://127.0.0.1:{port}/callback")),
            ("state", &state),
            ("code_challenge", &challenge),
        ],
    )
    .map_err(|e| AuthError::Server(e.to_string()))?
    .to_string();
    Ok(LoginRequest { url, listener, state, verifier })
}

const DONE_PAGE: &str = "<!doctype html><meta charset=utf-8><title>NextReply</title>\
<body style=\"font-family:system-ui,'Microsoft YaHei';display:flex;align-items:center;justify-content:center;height:100vh;margin:0\">\
<div style=\"text-align:center\"><h2>登录成功</h2><p>可以关闭此页面，回到 NextReply。</p></div></body>";

fn respond(mut stream: TcpStream, status: &str, body: &str) {
    let _ = write!(
        stream,
        "HTTP/1.1 {status}\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
}

/// 阻塞等待浏览器回调，返回授权码。
fn wait_for_code(listener: &TcpListener, expected_state: &str) -> Result<String, AuthError> {
    listener.set_nonblocking(true).map_err(|e| AuthError::Server(e.to_string()))?;
    let deadline = Instant::now() + LOGIN_TIMEOUT;
    while Instant::now() < deadline {
        let (stream, _) = match listener.accept() {
            Ok(s) => s,
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                std::thread::sleep(Duration::from_millis(100));
                continue;
            }
            Err(e) => return Err(AuthError::Server(e.to_string())),
        };
        let _ = stream.set_nonblocking(false);
        let _ = stream.set_read_timeout(Some(Duration::from_secs(5)));
        let mut line = String::new();
        if BufReader::new(&stream).read_line(&mut line).is_err() {
            continue;
        }
        // "GET /callback?code=...&state=... HTTP/1.1"
        let path = line.split_whitespace().nth(1).unwrap_or("");
        let Ok(url) = reqwest::Url::parse(&format!("http://127.0.0.1{path}")) else { continue };
        if url.path() != "/callback" {
            respond(stream, "404 Not Found", "");
            continue;
        }
        let param = |k: &str| url.query_pairs().find(|(name, _)| name == k).map(|(_, v)| v.into_owned());
        if param("state").as_deref() != Some(expected_state) {
            respond(stream, "400 Bad Request", "登录状态不匹配，请回到 NextReply 重新登录。");
            continue;
        }
        let Some(code) = param("code") else {
            respond(stream, "400 Bad Request", "缺少授权码。");
            continue;
        };
        respond(stream, "200 OK", DONE_PAGE);
        return Ok(code);
    }
    Err(AuthError::Server("登录超时，请重新点击登录。".into()))
}

/// 等待回调并换取令牌。成功后令牌已保存。
pub async fn complete(creds: &Credentials, req: LoginRequest) -> Result<(), AuthError> {
    let LoginRequest { listener, state, verifier, .. } = req;
    let code = tauri::async_runtime::spawn_blocking(move || wait_for_code(&listener, &state))
        .await
        .map_err(|e| AuthError::Server(e.to_string()))??;

    let resp = creds
        .http
        .post(creds.url("/v1/auth/token"))
        .json(&json!({ "code": code, "code_verifier": verifier, "device_id": creds.device_id }))
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| AuthError::Network(e.to_string()))?;
    if !resp.status().is_success() {
        return Err(AuthError::Server(format!("登录失败（{}），请重试。", resp.status())));
    }
    let tokens = resp.json::<TokenResponse>().await.map_err(|e| AuthError::Network(e.to_string()))?;
    creds.store_tokens(&tokens);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Read;

    fn get(port: u16, path: &str) -> String {
        let mut s = TcpStream::connect(("127.0.0.1", port)).unwrap();
        write!(s, "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n").unwrap();
        let mut out = String::new();
        let _ = s.read_to_string(&mut out);
        out
    }

    #[test]
    fn callback_requires_matching_state() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let client = std::thread::spawn(move || {
            let favicon = get(port, "/favicon.ico");
            let forged = get(port, "/callback?code=evil&state=wrong");
            let ok = get(port, "/callback?code=abc123&state=expected");
            (favicon, forged, ok)
        });
        let code = wait_for_code(&listener, "expected").unwrap();
        let (favicon, forged, ok) = client.join().unwrap();
        assert_eq!(code, "abc123");
        assert!(favicon.starts_with("HTTP/1.1 404"));
        assert!(forged.starts_with("HTTP/1.1 400"));
        assert!(ok.starts_with("HTTP/1.1 200") && ok.contains("登录成功"));
    }

    #[test]
    fn pkce_verifier_is_long_enough() {
        // RFC 7636：verifier 长度 43–128
        let v = random_b64url(32);
        assert_eq!(v.len(), 43);
        assert_ne!(v, random_b64url(32));
    }
}
