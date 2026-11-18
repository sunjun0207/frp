# frp 部署

外网访问 `https://prev-frp.xuandou.vip`，经公网机器转到内网 `127.0.0.1:3000`。

```text
手机 / 外网客户端
  -> 公网 121.55.247.111 上的 frps（443）
  -> 内网 frpc
  -> 内网 127.0.0.1:3000
```

公网只跑 frps，内网只跑 frpc。两边各构建自己的镜像。配置在构建时打进镜像，文件是 `conf/frps.toml` 和 `conf/frpc.toml`。

## 当前参数

| 项 | 值 |
| --- | --- |
| 公网 IP | `121.55.247.111` |
| 域名 | `prev-frp.xuandou.vip` |
| frpc 登录 frps | `7000`，TCP + TLS |
| 外网访问端口 | `443` |
| 内网接收 | `127.0.0.1:3000`，由该服务自己终结 TLS |
| 认证 | 两边 `auth.token` 相同，写在两份 toml 里 |
| 公网管理后台 | `http://121.55.247.111:7500`，账号 `admin`，密码见 `conf/frps.toml` 的 `webServer.password` |
| 内网管理后台 | `http://内网机器IP:7400`，账号 `admin`，密码见 `conf/frpc.toml` 的 `webServer.password` |

`prev-frp.xuandou.vip` 的 DNS 指向 `121.55.247.111`。

## 端口放行

云服务器改安全组和系统防火墙。内网服务器改本机防火墙。下面是当前这套配置实际要放行的端口。

### 云服务器入站

| 端口 | 协议 | 来源 | 用途 |
| --- | --- | --- | --- |
| `443` | TCP | 外网手机、浏览器 | `https://prev-frp.xuandou.vip` 和 `wss://prev-frp.xuandou.vip/ws` |
| `7000` | TCP | 内网服务器的出口 IP | frpc 登录 frps |
| `7500` | TCP | 需要看后台的管理员 IP | frps 管理后台 `http://121.55.247.111:7500` |

`80`、`6000`、`6001` 当前不用放行。只有按后文打开明文 HTTP 或 TCP/UDP 代理时，才额外放行对应端口。

### 内网服务器

frpc 主动连出到 `121.55.247.111:7000`，内网不用为 frp 对公网开放入站端口。

| 方向 | 端口 | 协议 | 用途 |
| --- | --- | --- | --- |
| 出站 | `121.55.247.111:7000` | TCP | frpc 连接云服务器上的 frps |
| 本机 | `127.0.0.1:3000` | TCP | 数据服务只监听本机，不对局域网或公网开放 |
| 入站，仅内网 | `7400` | TCP | frpc 管理后台 `http://内网机器IP:7400` |

内网防火墙如果限制出站，放行到 `121.55.247.111` 的 TCP `7000`。`7400` 只给内网访问，不要映射到公网。

## 公网：构建镜像并创建 frps 容器

在公网机器的本仓库根目录，先构建镜像：

```bash
docker build -f dockerfiles/Dockerfile-for-frps -t frps:local .
```

再创建并启动容器。`--network host` 让 frps 直接占用宿主机的 `7000` 和 `443`。同名容器已存在时，先删掉再创建：

```bash
docker rm -f frps
docker run -d --name frps --restart unless-stopped --network host frps:local
```

这条命令会创建名为 `frps` 的容器。镜像里的启动参数是 `frps -c /etc/frp/frps.toml`。创建后确认它在运行：

```bash
docker ps --filter name=frps
docker logs frps
```

日志里要有 `frps started successfully`，以及 `listen on 0.0.0.0:7000` 和 `listen on 0.0.0.0:443`。`443` 已被其它进程占用时，这条创建会失败。

## TLS 证书

手机访问 `https://prev-frp.xuandou.vip` 和 `wss://prev-frp.xuandou.vip/ws` 时，证书由内网 `127.0.0.1:3000` 上的数据服务提供。frps 只按域名把 TLS 连接转过去，不读取这张证书。证书和私钥不要放进 frps、frpc 镜像，也不要放到公网机器。

frpc 连接 frps 的 `7000` 端口是另一段 TLS。当前配置没有填写 `certFile`，frps 每次启动自己生成临时证书，不用准备文件，也不用续期。

### 放置

证书签给 `prev-frp.xuandou.vip`。两个文件放在内网机器：

```text
/opt/xuandou/certs/fullchain.pem
/opt/xuandou/certs/privkey.pem
```

`fullchain.pem` 是证书链，`privkey.pem` 是私钥。目录权限只给运行数据服务的用户读取。

### 使用

数据服务先于 frpc 启动，并在 `127.0.0.1:3000` 上用这两个文件做 TLS。Docker 启动时把宿主机目录只读挂进该容器，服务配置里的证书路径写成容器内路径 `/certs/fullchain.pem` 和 `/certs/privkey.pem`：

```bash
docker run -d --name xuandou-api --restart unless-stopped \
  -v /opt/xuandou/certs:/certs:ro \
  -p 127.0.0.1:3000:3000 \
  your-api-image
```

外网请求到达后，手机校验的是这张 `prev-frp.xuandou.vip` 证书。HTTPS 的 JSON、文件和 WSS 都走同一张证书。

证书到期前替换这两个文件，然后只重启 `xuandou-api`。frps 和 frpc 不用改配置，也不用重新构建。新证书生效前，手机会报证书错误，数据传不进来。

## 内网：构建镜像并创建 frpc 容器

数据服务已经在 `127.0.0.1:3000` 监听后，在内网机器的本仓库根目录构建镜像：

```bash
docker build -f dockerfiles/Dockerfile-for-frpc -t frpc:local .
```

创建并启动容器：

```bash
docker rm -f frpc
docker run -d --name frpc --restart unless-stopped --network host frpc:local
```

这条命令会创建名为 `frpc` 的容器。镜像里的启动参数是 `frpc -c /etc/frp/frpc.toml`。`loginFailExit = false`，连不上公网时会继续重试，间隔最长约 20 秒。创建后确认：

```bash
docker ps --filter name=frpc
docker logs frpc
```

日志里要有 `login to server success` 和 `[user-data-api] start proxy success`。

## 改配置后怎么生效

改 `conf/frps.toml` 或 `conf/frpc.toml` 后重新构建对应镜像，再执行上面的 `docker rm` 和 `docker run`。

不想重新构建时，启动命令加上挂载，覆盖镜像里的配置：

```bash
docker run -d --name frps --restart unless-stopped --network host \
  -v /opt/frp/frps.toml:/etc/frp/frps.toml:ro \
  frps:local

docker run -d --name frpc --restart unless-stopped --network host \
  -v /opt/frp/frpc.toml:/etc/frp/frpc.toml:ro \
  frpc:local
```

## 接入接口

frp 没有提交数据的页面。当前 `conf/frpc.toml` 只启用了 HTTPS 代理，外网用 `https://` 或 `wss://`，没有单独的明文 `http://` 接口。流量原样转到内网 `127.0.0.1:3000`，由这个服务终结 TLS 后再按路径接收。

内网当时没接住的数据不会留下。frps 和 frpc 不落盘、不补发。服务恢复后只能收到新的请求，之前没接到的内容要外网再发一次。WSS 断开后由客户端自己重连并重发未送达的数据。frpc 到 frps 的隧道会自动重连，已断开的 WSS 会话不会续上。

### HTTPS JSON

外网发送：

```http
POST /v1/events HTTP/1.1
Host: prev-frp.xuandou.vip
Content-Type: application/json

{"userId":"u1","text":"hello"}
```

地址是 `https://prev-frp.xuandou.vip/v1/events`。

内网 `127.0.0.1:3000` 在 TLS 解密后收到同一条请求：

```http
POST /v1/events HTTP/1.1
Host: prev-frp.xuandou.vip
Content-Type: application/json

{"userId":"u1","text":"hello"}
```

内网服务读 body，处理完后写回 HTTP 响应。响应会沿原连接回到外网。

### HTTPS 文件

外网发送：

```http
POST /v1/files HTTP/1.1
Host: prev-frp.xuandou.vip
Content-Type: multipart/form-data; boundary=----boundary

------boundary
Content-Disposition: form-data; name="file"; filename="a.mp4"
Content-Type: video/mp4

<二进制内容>
------boundary--
```

地址是 `https://prev-frp.xuandou.vip/v1/files`。图片、音频、视频文件都走这个接口，用各自的 `Content-Type`。

内网 `127.0.0.1:3000` 收到同一个 `POST /v1/files`，按 `multipart/form-data` 解析文件并保存。

### WSS

外网连接 `wss://prev-frp.xuandou.vip/ws`。字符串走文本帧，实时音视频走二进制帧。

内网 `127.0.0.1:3000` 先收到升级请求，同意后按帧接收：

```http
GET /ws HTTP/1.1
Host: prev-frp.xuandou.vip
Upgrade: websocket
Connection: Upgrade
```

这条 WSS 和上面的 HTTPS 共用当前这一个 `type = "https"` 代理，不用另开端口。连接断开后再连就是一次新的 `GET /ws`，之前没读完的帧不会补发。

### 其它协议

`tcp`、`udp`、明文 `http`、`stcp`、`xtcp`、`sudp` 还没有写进 `conf/frpc.toml`。要接入就在该文件追加对应代理，然后重新构建 frpc，或按上一节挂载新配置后重启 frpc。明文 HTTP 还要在 `conf/frps.toml` 增加 `vhostHTTPPort`，并重新构建 frps。

TCP，外网连 `121.55.247.111:6000`，内网在 `127.0.0.1:3001` 接收原始字节：

```toml
[[proxies]]
name = "user-data-tcp"
type = "tcp"
localIP = "127.0.0.1"
localPort = 3001
remotePort = 6000
```

UDP，外网向 `121.55.247.111:6001` 发数据报，内网在 `127.0.0.1:3002` 接收：

```toml
[[proxies]]
name = "user-data-udp"
type = "udp"
localIP = "127.0.0.1"
localPort = 3002
remotePort = 6001
```

明文 HTTP 和明文 WebSocket。外网访问 `http://prev-frp.xuandou.vip/v1/events`，或连接 `ws://prev-frp.xuandou.vip/ws`。frps 增加 `vhostHTTPPort = 80`，frpc 追加：

```toml
[[proxies]]
name = "user-data-http"
type = "http"
localIP = "127.0.0.1"
localPort = 3000
customDomains = ["prev-frp.xuandou.vip"]
```

内网 `127.0.0.1:3000` 收到的路径与 HTTPS 相同，这条连接没有 TLS。公网 `80` 端口已被占用时，把 `vhostHTTPPort` 改成空闲端口，外网地址带上该端口。

`stcp`、`xtcp`、`sudp` 是带密钥或打洞的点对点代理，访问方不是开放的公网端口。需要时再追加对应的 `[[proxies]]` 和访问端配置。

## 本机验证

2026-11-18 在 Windows 上的 Docker Desktop 验证通过。两个镜像可以构建，容器可以启动，frpc 能登录 frps，并注册 HTTPS 代理 `user-data-api`。

Docker Desktop 没有 Linux 那种宿主机网络，所以本机不要用上面的 `--network host`。在本仓库根目录创建桥接网络和两个容器。宿主机端口对应关系：`17000 -> 7000`、`18443 -> 443`、`17500 -> 7500`、`17400 -> 7400`。已构建的镜像里可能还是旧配置，所以本机把当前 toml 挂进去：

```bash
docker network create frp-local
docker rm -f frps frpc
docker run -d --name frps --restart unless-stopped --network frp-local \
  -p 17000:7000 -p 18443:443 -p 17500:7500 \
  -v "$(pwd)/conf/frps.toml:/etc/frp/frps.toml:ro" \
  frps:local
```

本机 frpc 要连同一网络里的 frps，把 `conf/frpc.toml` 复制出来，只把 `serverAddr` 改成 `frps`，不要改仓库里的原文件。挂载不要加 `:ro`，这样本机后台可以保存配置：

```bash
mkdir -p /tmp/frp-local-test
sed 's/serverAddr = "121.55.247.111"/serverAddr = "frps"/' conf/frpc.toml > /tmp/frp-local-test/frpc.toml
docker run -d --name frpc --restart unless-stopped --network frp-local \
  -p 17400:7400 \
  -v /tmp/frp-local-test/frpc.toml:/etc/frp/frpc.toml \
  frpc:local
```

Windows PowerShell 用下面这组创建 frpc 容器：

```powershell
$dir = Join-Path $env:TEMP "frp-local-test"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
(Get-Content conf\frpc.toml -Raw).Replace('serverAddr = "121.55.247.111"', 'serverAddr = "frps"') | Set-Content (Join-Path $dir "frpc.toml") -Encoding ascii
docker run -d --name frpc --restart unless-stopped --network frp-local -p 17400:7400 -v "${dir}/frpc.toml:/etc/frp/frpc.toml" frpc:local
```

正式部署到公网和内网时，仍使用前面的 `--network host`，`serverAddr` 仍是 `121.55.247.111`。

本机构建时，容器访问 `proxy.golang.org` 超时。当时的构建命令额外指定了 `GOPROXY=https://goproxy.cn,direct`。仓库里的 Dockerfile 没有改。服务器如果能访问 `proxy.golang.org`，直接用前面的 `docker build` 即可。

验证时的日志：

```text
frps tcp listen on 0.0.0.0:7000
https service listen on 0.0.0.0:443
frps started successfully
client login info: ip [172.20.0.3:43564] version [0.72.0]
[user-data-api] https proxy listen for host [prev-frp.xuandou.vip]
new proxy [user-data-api] type [https] success
frpc login to server success
[user-data-api] start proxy success
```

本机容器还在运行。不再需要时执行：

```bash
docker rm -f frps frpc
docker network rm frp-local
```

## Web 管理后台

两份配置已经打开管理后台。后台不接收业务数据。发数据仍用 `https://prev-frp.xuandou.vip` 和 `wss://prev-frp.xuandou.vip/ws`。后台用来查看转发是否正常，以及在内网修改 frpc 配置。

改过 toml 后要重新构建对应镜像，再按前面的命令创建容器。容器创建后用浏览器登录。

### 公网 frps：查看

浏览器打开 `http://121.55.247.111:7500`。用户名 `admin`，密码是 `conf/frps.toml` 里的 `webServer.password`。云服务器要放行 TCP `7500`。

登录后可以做这些事：

- 总览页看 frps 是否在运行、在线客户端数量和代理数量。
- Clients 页看内网 frpc 有没有登录，点进一台可以看到它登记的代理。
- Proxies 页看 `user-data-api` 是否在线，点进去可以看这个 HTTPS 代理的流量。
- 清理已经下线的代理记录。

这个页面不能改 token、端口、域名，也不能改 `frps.toml`。

本机 Docker Desktop 的地址是 `http://127.0.0.1:17500`，账号相同。

### 内网 frpc：修改配置

浏览器打开 `http://内网机器IP:7400`。用户名 `admin`，密码是 `conf/frpc.toml` 里的 `webServer.password`。只在内网放行 TCP `7400`。

登录后可以做这些事：

- 代理列表里查看、编辑 `user-data-api`，例如本地端口和域名。
- 打开配置编辑页，直接改整份配置，包括 `serverAddr` 和 `auth.token`。
- 保存后，frpc 把内容写回正在使用的配置文件并重新加载，不用手工重启容器。

配置文件必须可写挂载，否则页面保存失败。镜像里自带的文件在删除容器后会丢失。正式环境用下面的命令创建 frpc，不要加 `:ro`：

```bash
docker run -d --name frpc --restart unless-stopped --network host \
  -v /opt/frp/frpc.toml:/etc/frp/frpc.toml \
  frpc:local
```

## 查看状态

```bash
docker logs -f frps
docker logs -f frpc
```

frpc 日志出现 `login to server success` 后，外网才能访问 `https://prev-frp.xuandou.vip`。
