//! 最小 HTTP/1.1 客户端：只用来对 loopback 的 helper 发一个 POST。
//!
//! 不引 reqwest/hyper：这里只需要"连上、发一行动词、读回状态码"，为了一个
//! 退出请求增加一整棵依赖树（连带 TLS 栈）不划算。请求体固定为空，响应体
//! 不解析——调用方只关心 200 / 404 / 超时三种结果。

use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::time::{Duration, Instant};

pub struct HelperClient {
    addr: SocketAddr,
    token: String,
    timeout: Duration,
}

#[derive(Debug, PartialEq)]
pub struct Response {
    pub status: u16,
}

impl HelperClient {
    pub fn new(port: u16, token: String, timeout: Duration) -> Self {
        Self {
            // helper 只监听 loopback；这里也钉死 127.0.0.1，避免 DNS 参与。
            addr: SocketAddr::from(([127, 0, 0, 1], port)),
            token,
            timeout,
        }
    }

    pub fn post(&self, path: &str) -> Result<Response, String> {
        let mut stream = TcpStream::connect_timeout(&self.addr, self.timeout)
            .map_err(|err| format!("connect to helper: {err}"))?;
        // 两个超时都设上：写超时管"对端不读"，读超时管"对端不回"。
        let _ = stream.set_write_timeout(Some(self.timeout));
        let _ = stream.set_read_timeout(Some(self.timeout));
        stream
            .write_all(build_request(path, &self.token).as_bytes())
            .map_err(|err| format!("write request: {err}"))?;
        let _ = stream.flush();

        let deadline = Instant::now() + self.timeout;
        let mut raw = Vec::new();
        let mut chunk = [0u8; 512];
        loop {
            if find_header_end(&raw).is_some() {
                break;
            }
            if Instant::now() >= deadline {
                return Err("timed out waiting for the helper's response".into());
            }
            match stream.read(&mut chunk) {
                Ok(0) => break,
                Ok(read) => raw.extend_from_slice(&chunk[..read]),
                Err(err) => return Err(format!("read response: {err}")),
            }
        }
        parse_response(&raw)
    }
}

fn build_request(path: &str, token: &str) -> String {
    format!(
        "POST {path} HTTP/1.1\r\n\
         Host: 127.0.0.1\r\n\
         Authorization: Bearer {token}\r\n\
         Content-Length: 0\r\n\
         Connection: close\r\n\
         \r\n"
    )
}

fn find_header_end(raw: &[u8]) -> Option<usize> {
    raw.windows(4).position(|window| window == b"\r\n\r\n")
}

/// 只解析状态行：调用方要的是"helper 有没有接受"，不是响应体。
pub fn parse_response(raw: &[u8]) -> Result<Response, String> {
    let head_end = find_header_end(raw).ok_or("malformed HTTP response (no header terminator)")?;
    let head = String::from_utf8_lossy(&raw[..head_end]);
    let status_line = head.lines().next().ok_or("empty HTTP response")?;
    let mut parts = status_line.split_whitespace();
    let version = parts.next().unwrap_or_default();
    if !version.starts_with("HTTP/") {
        return Err(format!("not an HTTP response: {status_line}"));
    }
    let status = parts
        .next()
        .ok_or("missing status code")?
        .parse::<u16>()
        .map_err(|_| format!("bad status code: {status_line}"))?;
    Ok(Response { status })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::TcpListener;
    use std::thread;

    #[test]
    fn parses_status_line() {
        assert_eq!(parse_response(b"HTTP/1.1 200 OK\r\n\r\n").unwrap().status, 200);
        assert_eq!(parse_response(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n").unwrap().status, 404);
        assert_eq!(parse_response(b"HTTP/1.1 409 Conflict\r\n\r\n").unwrap().status, 409);
    }

    #[test]
    fn rejects_garbage() {
        assert!(parse_response(b"").is_err());
        assert!(parse_response(b"OK\r\n\r\n").is_err());
        assert!(parse_response(b"HTTP/1.1 abc\r\n\r\n").is_err());
        assert!(parse_response(b"HTTP/1.1 200 OK\r\n").is_err());
    }

    #[test]
    fn posts_to_a_listening_socket() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let port = listener.local_addr().unwrap().port();
        let server = thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            let mut buffer = [0u8; 512];
            let _ = socket.read(&mut buffer);
            let _ = socket.write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok");
        });
        let client = HelperClient::new(port, "token".into(), Duration::from_secs(2));
        let response = client.post("/api/v1/shutdown").expect("request must succeed");
        assert_eq!(response.status, 200);
        server.join().unwrap();
    }

    // 对端接受连接却永不回包：必须在超时内返回错误，不能挂住退出流程。
    #[test]
    fn times_out_when_the_helper_never_answers() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let port = listener.local_addr().unwrap().port();
        let server = thread::spawn(move || {
            let (socket, _) = listener.accept().unwrap();
            thread::sleep(Duration::from_millis(600));
            drop(socket);
        });
        let client = HelperClient::new(port, "token".into(), Duration::from_millis(150));
        let started = Instant::now();
        assert!(client.post("/api/v1/shutdown").is_err());
        assert!(started.elapsed() < Duration::from_millis(500), "must respect its timeout");
        server.join().unwrap();
    }
}
