# Better Real Me 分享服务

为 AstrBot 的 Better Real Me 插件提供独立的公网只读分享页面。你可以选择公开的生活信息范围；分享默认关闭。访客打开页面时，服务才向在线插件请求一次最新快照，并保存每个连接的最新快照。

- **Linux 安装包：**[下载 v1.0.0](https://github.com/Nortea575/better_real_me_share/releases/tag/v1.0.0)
- **服务源码：**[Nortea575/better_real_me_share](https://github.com/Nortea575/better_real_me_share)
- **AstrBot 插件：**[Nortea575/astrbot_plugin_better_real_me](https://github.com/Nortea575/astrbot_plugin_better_real_me)

## 功能

- 为每个已绑定机器人提供独立的公开链接。
- 访客访问时刷新最新快照；同时访问会合并为一次刷新。
- 插件离线时展示最近保存的快照；没有访客时不会导出或上传数据。
- 分享默认关闭，可从插件或管理页面关闭。

## 快速开始：Linux 原生安装

需要 Linux `amd64` 或 `arm64` 与 `systemd`。安装包内含服务程序、SQLite 存储和网页资源，不需要另外安装 Go、Node.js 或数据库服务。

1. 从[发布页](https://github.com/Nortea575/better_real_me_share/releases/tag/v1.0.0)下载 `better_real_me_share-1.0.0-linux.tar.gz` 并解压：

   ```sh
   tar -xzf better_real_me_share-1.0.0-linux.tar.gz
   cd better_real_me_share
   ```

2. 用服务器可访问的 IP 地址先完成 HTTP 连接，或按下文配置 HTTPS。把示例地址换成自己的地址：

   ```sh
   sudo env REAL_ME_PUBLIC_URL="http://YOUR_SERVER_IP:18766/" sh ./install.sh
   ```

   安装器默认监听 `0.0.0.0:18766`。请确保你的网络环境允许访客访问所选服务端口。程序和数据保存在 `/opt/real_me_add`；安装器只管理自己的 `real-me-add.service`。

3. 安装器会在终端显示一次性管理员设置链接。私下打开该链接并设置管理员密码；链接完成设置后立即失效。
4. 登录管理页面，生成并复制一次性绑定链接，粘贴到插件的公网分享设置中。链接有效期为 20 分钟。
5. 在插件中选择公开范围并启用分享，再打开管理页面给出的公开链接。每次访客访问时，在线插件会生成并上传一份新的快照；插件未连接时，服务会返回最近一次保存的数据。

查看服务状态：

```sh
sudo systemctl status real-me-add.service
```

升级时下载新安装包并再次运行 `install.sh`。安装器只更新它管理的服务文件和程序，并重启该服务。

## 使用已有 HTTPS 网关

若已有反向代理，可让它通过本机回环地址访问服务，并将用户访问的 HTTPS 地址作为公开 URL：

```sh
sudo env \
  REAL_ME_PUBLIC_URL="https://share.example.com/" \
  REAL_ME_LISTEN="127.0.0.1:18766" \
  sh ./install.sh
```

在网关中把该域名单独转发到 `http://127.0.0.1:18766/`。如果网关运行在容器中，容器内的 `127.0.0.1` 指向容器自身；请将网关配置到容器可访问的宿主机地址，并让服务监听该地址。

也可以让本服务直接提供 HTTPS。访客 URL 应包含实际监听端口：

```sh
sudo env \
  REAL_ME_PUBLIC_URL="https://share.example.com:18766/" \
  REAL_ME_TLS_CERT="/path/to/fullchain.pem" \
  REAL_ME_TLS_KEY="/path/to/private-key.pem" \
  sh ./install.sh
```

安装器不会配置反向代理、证书、防火墙或其他服务。

## Docker Compose

Docker Compose 适用于从 Linux 发布包解压出的目录。发布包已包含 Dockerfile 所需的预编译 `linux-amd64` 与 `linux-arm64` 程序。宿主机端口绑定到回环地址，HTTPS 由你配置的网关提供：

```sh
export REAL_ME_PUBLIC_URL="https://share.example.com/"
docker compose up -d --build
docker compose exec share /brm-share setup-link --data-dir /data --public-url "$REAL_ME_PUBLIC_URL"
```

网关与容器在同一宿主机时，可将上游指向宿主机 `127.0.0.1:18766`；容器网关需要使用容器可访问的宿主机地址。持久数据保存在 `share_data` 卷中。

## 从源码构建

源码仓库需要 Go `1.27.0` 或更新版本。它包含 Go 源码、测试和网页资源，不包含预编译服务程序：

```sh
git clone https://github.com/Nortea575/better_real_me_share.git
cd better_real_me_share
go test ./...
go build -trimpath -o brm-share .
```

Docker Compose 使用的 Dockerfile 需要预编译架构程序；请从 Linux 发布包运行 Compose。仅从源码仓库构建时，使用上面的 Go 命令构建服务。

## 作者

Xing