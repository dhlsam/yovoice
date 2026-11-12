# syntax=docker/dockerfile:1
# yovoice 网页版镜像：浏览器直接访问界面与接口，不需要桌面宿主。
# 构建：docker build -f deploy/Dockerfile -t yovoice .
# 运行：
#   docker run -d --name yovoice -p 8080:8080 \
#     -e YOVOICE_WEB_TOKEN="$(openssl rand -hex 32)" \
#     -v yovoice-data:/data yovoice
# 打开 http://localhost:8080 ，令牌取自启动日志里的 loginUrl。
# 命令行接口与 MCP 仍可用：docker run -i --rm -v yovoice-data:/data yovoice mcp --data-dir /data

# 第一阶段：构建网页版前端。界面直接引用 internal/ 下的模型目录等数据，所以要一并复制。
FROM node:22-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
COPY web/patches ./patches
RUN corepack enable && corepack prepare pnpm@9.15.2 --activate \
    && pnpm install --frozen-lockfile
COPY web/src ./src
COPY web/public ./public
COPY web/index.html web/tsconfig.json web/vite.config.ts ./
COPY internal/ /src/internal/
RUN pnpm run build

# 第二阶段：编译服务端与随包转换器。
FROM golang:1.26-bookworm AS builder
RUN apt-get update && apt-get install -y --no-install-recommends \
      python3 make gcc curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ cmd/
COPY internal/ internal/
COPY scripts/ scripts/
RUN python3 scripts/build-ffmpeg.py artifacts/tools \
    && go build -trimpath -ldflags "-s -w" -o /out/yovoice ./cmd/yovoice \
    && cp -r artifacts/tools /out/tools

# 第三阶段：运行镜像。
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/yovoice /usr/local/bin/yovoice
COPY --from=builder /out/tools /usr/local/bin/tools
COPY --from=web /src/web/dist /opt/yovoice/web
ENV YOVOICE_WEB_DIR=/opt/yovoice/web
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["yovoice"]
CMD ["web", "--data-dir", "/data", "--listen", "0.0.0.0:8080", "--web-dir", "/opt/yovoice/web"]
