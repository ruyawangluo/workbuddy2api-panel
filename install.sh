#!/usr/bin/env bash
# 一键部署脚本：Docker Compose（方式一）
# 用法：在项目根目录执行 ./install.sh
set -euo pipefail

cd "$(dirname "$0")"

PORT=7863

# ── 1. 环境检查 ──────────────────────────────────────────
if ! command -v docker >/dev/null 2>&1; then
    echo "错误：未检测到 docker，请先安装 Docker。" >&2
    exit 1
fi

if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE="docker-compose"
else
    echo "错误：未检测到 docker compose，请先安装 Docker Compose。" >&2
    exit 1
fi

# ── 2. 准备配置与数据目录 ────────────────────────────────
if [ ! -f config.json ]; then
    cp config.example.json config.json
    echo "已生成 config.json（默认 API Key：admin）"
else
    echo "检测到已有 config.json，保持不变"
fi
mkdir -p auths data

# ── 3. 构建并启动 ────────────────────────────────────────
echo "正在构建并启动容器（首次构建约 1-2 分钟）..."
$COMPOSE up -d --build

# ── 4. 等待服务就绪（无账号时 /healthz 返回 503，连接成功即视为就绪） ──
echo "等待服务就绪..."
ready=0
for _ in {1..30}; do
    if curl -s -o /dev/null --max-time 3 "http://127.0.0.1:${PORT}/healthz"; then
        ready=1
        break
    fi
    sleep 2
done
if [ "$ready" -ne 1 ]; then
    echo "警告：健康检查超时，请用 '$COMPOSE logs -f' 查看日志。" >&2
fi

# ── 5. 完成提示 ──────────────────────────────────────────
echo ""
echo "部署完成。"
echo ""
echo "  面板地址:     http://<服务器IP>:${PORT}/panel/"
echo "  API Key:      admin"
echo ""
echo "首次登录请使用 API Key：admin，登录后请立即在面板中修改。"
echo ""
echo "复制以下命令可直接在本机验证（API Key 已内置 admin）："
echo "  curl -s http://127.0.0.1:${PORT}/status -H \"Authorization: Bearer admin\""
echo ""
echo "查看日志: $COMPOSE logs -f"
