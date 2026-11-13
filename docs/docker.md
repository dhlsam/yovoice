# Web version and Docker deployment

English · [简体中文](docker_zh.md)

Besides the desktop app, yovoice runs as a web version on a server: open the address in a browser and you get the full creation workspace, while inference stays on the machine. The images ship the web frontend and the inference service; NVIDIA GPUs work with `--gpus all`.

## Quick start

CPU:

```sh
docker build -f deploy/Dockerfile -t yovoice .

docker run -d --name yovoice \
  -p 8080:8080 \
  -e YOVOICE_WEB_TOKEN="$(openssl rand -hex 32)" \
  -v yovoice-data:/data \
  yovoice
```

Open <http://localhost:8080> and sign in with the token from the startup log:

```sh
docker logs yovoice
```

The log prints a JSON line whose `loginUrl` already carries the token — open it and you are signed in. Without `YOVOICE_WEB_TOKEN` a random token is generated on every start, so the link changes after a restart.

With compose:

```sh
export YOVOICE_WEB_TOKEN="$(openssl rand -hex 32)"
docker compose -f deploy/docker-compose.yml up -d yovoice
```

## NVIDIA GPUs (CUDA)

Requirements: an NVIDIA driver on the host plus [nvidia-container-toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html), and the driver must support CUDA 12.8.

```sh
docker build -f deploy/Dockerfile.cuda -t yovoice:cu128 .

docker run -d --name yovoice --gpus all \
  -p 8080:8080 \
  -e YOVOICE_WEB_TOKEN="$(openssl rand -hex 32)" \
  -v yovoice-data:/data \
  yovoice:cu128
```

The CUDA image ships the CUDA 12.8 engine, so the GPU is used from the first launch and no engine download is needed. Build with `--build-arg WITH_CUDART=0` to use the CUDA libraries already on the host instead of the bundled ones (about 800 MB smaller image).

With compose:

```sh
docker compose -f deploy/docker-compose.yml up -d yovoice-gpu
```

The GPU service listens on 8081 so it does not clash with the CPU one.

On AMD or Intel GPUs, use the same image and pick the “Vulkan” backend in Settings → Inference engine.

## Configuration

| Variable | Purpose |
| --- | --- |
| `YOVOICE_WEB_TOKEN` | Access token, at least 32 characters. A random one is generated when unset |
| `YOVOICE_WEB_DIR` | Web frontend directory; the images point it at `/opt/yovoice/web` |
| `WORKBENCH_ENGINE` | Preinstalled engine executable; the CUDA image points it at the bundled engine |

Command line flags of `yovoice web`:

| Flag | Purpose |
| --- | --- |
| `--listen` | Listen address, default `0.0.0.0:8080` |
| `--web-dir` | Frontend directory, default `web/dist` |
| `--token` / `--token-file` | Access token, or read it from a file |
| `--tls-cert` / `--tls-key` | Serve over HTTPS |

## Data and models

The `/data` volume holds projects, models, voices and installed engines. Rebuilding the container keeps the data as long as the volume is kept:

```sh
docker volume ls          # list volumes
docker run --rm -v yovoice-data:/data -v "$PWD":/backup yovoice tar czf /backup/yovoice-backup.tar.gz -C /data .
```

Models are downloaded from Settings → Models in the web version, from ModelScope, Hugging Face or a mirror, exactly like on the desktop. Put the volume on an SSD: the first model download is large.

## Behind a reverse proxy

The web version has its own token login; let the proxy terminate TLS:

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

Long generations can run for a long time, so raise `proxy_read_timeout`; state updates use an SSE stream, so `proxy_buffering off` is required.

## Resources

- CPU: at least 4 cores and 8 GB of memory; large models need more.
- GPU: pick the model by VRAM — IndexTTS 2.5 Q8 needs about 4 GB, VoxCPM2 about 8 GB, ACE-Step starts around 12 GB.
- Model downloads and generations fill the disk; reserve 50 GB or more for `/data`.

## Relation to the desktop app

The web version and the desktop app share the same data directory format (`~/.yovoice`, `/data` in the container). You can download models on the desktop and copy the directory to the server, or the other way around. Both use the same state database, so running them at the same time locks the directory and the second process exits with an error.

## Troubleshooting

**The page spins or returns 401.** The token does not match. The startup log prints a `loginUrl` that carries the token — open it to store the login cookie. Each browser signs in separately.

**“Engine missing” while generating.** Like on the desktop, install the engine once in Settings → Inference engine. The CUDA image already includes it: pick “NVIDIA GPU · CUDA 12.8”.

**No GPU inside the container.** The host is missing nvidia-container-toolkit, or `--gpus all` was not passed. Check with `docker run --rm --gpus all nvidia/cuda:12.8.1-runtime-ubuntu24.04 nvidia-smi`.

**Using the CLI or MCP.** The image entry point is `yovoice`, so the subcommand can be replaced:

```sh
docker run --rm -v yovoice-data:/data yovoice status --data-dir /data
docker run -i --rm -v yovoice-data:/data yovoice mcp --data-dir /data
```
