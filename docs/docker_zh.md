# 网页版与 Docker 部署

简体中文 · [English](docker.md)

yovoice 除了桌面应用，还可以在服务器上以网页版运行：浏览器打开地址就是完整的创作界面，推理在服务器本地完成，模型和数据都不出机器。镜像内置网页版前端与推理服务，NVIDIA 显卡用 `--gpus all` 即可。

## 快速开始

CPU 版：

```sh
docker build -f deploy/Dockerfile -t yovoice .

docker run -d --name yovoice \
  -p 8080:8080 \
  -e YOVOICE_WEB_TOKEN="$(openssl rand -hex 32)" \
  -v yovoice-data:/data \
  yovoice
```

打开 <http://localhost:8080>，用启动日志里的令牌登录：

```sh
docker logs yovoice
```

日志里会打印一行 JSON，`loginUrl` 是带令牌的登录链接，直接打开即可，不必手动输入令牌。没有设置 `YOVOICE_WEB_TOKEN` 时每次启动都会随机生成一个令牌，重启后需要重新登录。

也可以用 compose：

```sh
export YOVOICE_WEB_TOKEN="$(openssl rand -hex 32)"
docker compose -f deploy/docker-compose.yml up -d yovoice
```

## NVIDIA 显卡（CUDA）

前置条件：宿主机已安装 NVIDIA 驱动与 [nvidia-container-toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)，驱动要支持 CUDA 12.8。

```sh
docker build -f deploy/Dockerfile.cuda -t yovoice:cu128 .

docker run -d --name yovoice --gpus all \
  -p 8080:8080 \
  -e YOVOICE_WEB_TOKEN="$(openssl rand -hex 32)" \
  -v yovoice-data:/data \
  yovoice:cu128
```

CUDA 镜像内置 CUDA 12.8 推理内核，容器启动即用显卡，不需要再在设置里安装内核。想让镜像改用系统里的 CUDA 运行库（省下约 800 MB 镜像体积），构建时加 `--build-arg WITH_CUDART=0`。

用 compose 起显卡版：

```sh
docker compose -f deploy/docker-compose.yml up -d yovoice-gpu
```

显卡版默认监听 8081，避免和 CPU 版冲突。

没有 NVIDIA 显卡但显卡是 AMD 或 Intel 时，可以用 Vulkan 后端：镜像相同，在 设置 → 推理引擎 里选 “Vulkan” 并安装内核即可。

## 配置

| 环境变量 | 作用 |
| --- | --- |
| `YOVOICE_WEB_TOKEN` | 访问令牌，至少 32 个字符。不设置时每次启动随机生成 |
| `YOVOICE_WEB_DIR` | 网页版前端产物目录，镜像内已指向 `/opt/yovoice/web` |
| `WORKBENCH_ENGINE` | 预置推理内核的可执行文件路径，CUDA 镜像已指向内置内核 |

命令行参数（`yovoice web`）：

| 参数 | 说明 |
| --- | --- |
| `--listen` | 监听地址，默认 `0.0.0.0:8080` |
| `--web-dir` | 前端产物目录，默认 `web/dist` |
| `--token` / `--token-file` | 访问令牌，或从文件读取 |
| `--tls-cert` / `--tls-key` | 启用 HTTPS |

## 数据与模型

数据目录挂载在 `/data`，包含作品、模型、音色与已安装的内核。容器重建不会丢数据，只要不删这个卷：

```sh
docker volume ls          # 查看卷
docker run --rm -v yovoice-data:/data -v "$PWD":/backup yovoice tar czf /backup/yovoice-backup.tar.gz -C /data .
```

模型在网页版的 设置 → 模型 里下载，和桌面端一样从 ModelScope、HuggingFace 或镜像站获取。首次下载大模型时建议把卷放在 SSD 上。

## 部署在反向代理后面

网页版自带令牌登录，HTTPS 交给反向代理终止：

```nginx
server {
    listen 443 ssl;
    server_name yovoice.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;
        proxy_read_timeout 3600s;
        proxy_buffering off;
    }
}
```

长音频生成可能跑很久，`proxy_read_timeout` 要放宽；状态推送走的是 SSE 长连接，`proxy_buffering off` 不能省。

## 资源占用

- CPU 版建议至少 4 核、8 GB 内存；生成大模型时内存占用更高。
- 显卡版按模型选择显存：IndexTTS 2.5 Q8 约 4 GB，VoxCPM2 约 8 GB，ACE-Step 约 12 GB 起。
- 模型下载与生成都会占满磁盘，建议给 `/data` 预留 50 GB 以上。

## 与桌面端的关系

网页版和桌面应用共用同一套数据目录格式：`~/.yovoice`（容器里是 `/data`）。可以在桌面端下载模型，再把数据目录拷到服务器；也可以反过来。两者共用同一个 `yovoice` 状态库，同时打开会锁住数据目录，第二个进程会报错退出。

## 常见问题

**打开后一直转圈或报 401。** 令牌不对。用 `docker logs` 看启动时打印的 `loginUrl`，直接打开它会写入登录 Cookie。多个浏览器要分别登录。

**生成时提示找不到内核。** 网页版和桌面端一样，首次使用要在 设置 → 推理引擎 里安装内核。CUDA 镜像已经内置内核，直接选 “NVIDIA GPU · CUDA 12.8” 即可。

**容器里看不到显卡。** 宿主机没装 nvidia-container-toolkit，或者启动时忘了加 `--gpus all`。用 `docker run --rm --gpus all nvidia/cuda:12.8.1-runtime-ubuntu24.04 nvidia-smi` 验证。

**想用命令行接口或 MCP。** 镜像入口就是 `yovoice`，可以覆盖子命令：

```sh
docker run --rm -v yovoice-data:/data yovoice status --data-dir /data
docker run -i --rm -v yovoice-data:/data yovoice mcp --data-dir /data
```
