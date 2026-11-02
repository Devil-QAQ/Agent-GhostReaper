#!/bin/sh
# GhostReaper 本地靶场 · Vulhub 补充(特定高危漏洞按需起、打完即关)。
# 用法:lab/vulhub.sh fetch|up <环境>|down <环境>|list|clean
#   fetch       下载 Vulhub 官方仓库 tarball 并解压(不用 git;一次即可)
#   up <环境>   docker compose 起指定环境(如 lab/vulhub.sh up nginx/CVE-2021-23017)
#   down <环境> 关闭并清理该环境(打完必关:换环境不占端口不占内存)
#   list        列已下载的环境目录(compose 可起项)
#   clean       删已下载的 Vulhub 仓库与缓存
#
# 原则:Vulhub 是补充,主力是 DVWA(见 lab/dvwa.sh);环境地址以 compose
# 端口映射为准(起后 docker compose ps 看)。环境清单见 lab/vulhub/ 与
# https://vulhub.org(每个环境自带 README 与利用说明)。
set -eu

LAB_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
VULHUB_DIR="${LAB_DIR}/vulhub"
CACHE_DIR="${LAB_DIR}/.cache"
TARBALL="${CACHE_DIR}/vulhub-master.tar.gz"
TARBALL_URL="https://github.com/vulhub/vulhub/archive/refs/heads/master.tar.gz"

die() { echo "vulhub: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "缺依赖 $1"; }
need docker

compose() { docker compose "$@"; }

cmd=${1:-}
case "$cmd" in
fetch)
    need curl
    [ -f "${VULHUB_DIR}/.fetched" ] && { echo "vulhub: 已下载(${VULHUB_DIR})"; exit 0; }
    echo "vulhub: 下载 ${TARBALL_URL}(一次性,约几十 MB)"
    mkdir -p "$CACHE_DIR"
    curl -fL --retry 3 -o "$TARBALL" "$TARBALL_URL"
    rm -rf "$VULHUB_DIR"
    mkdir -p "$VULHUB_DIR"
    tar -xzf "$TARBALL" -C "$VULHUB_DIR" --strip-components=1
    touch "${VULHUB_DIR}/.fetched"
    rm -f "$TARBALL"
    echo "vulhub: 就绪(环境清单 lab/vulhub.sh list;也可浏览 ${VULHUB_DIR} 各环境 README)"
    ;;
up)
    env_dir=${2:-}
    [ -n "$env_dir" ] || die "用法: lab/vulhub.sh up <环境路径>(如 nginx/CVE-2021-23017)"
    [ -f "${VULHUB_DIR}/.fetched" ] || die "未下载,先执行 lab/vulhub.sh fetch"
    compose_file="${VULHUB_DIR}/${env_dir}/docker-compose.yml"
    [ -f "$compose_file" ] || [ -f "${VULHUB_DIR}/${env_dir}/compose.yaml" ] || die "环境不存在或无 compose 文件: ${env_dir}"
    echo "vulhub: 起 ${env_dir}"
    compose -f "$compose_file" up -d
    echo "vulhub: 已起(端口映射:)"
    compose -f "$compose_file" ps
    echo "vulhub: 打完执行 lab/vulhub.sh down ${env_dir}"
    ;;
down)
    env_dir=${2:-}
    [ -n "$env_dir" ] || die "用法: lab/vulhub.sh down <环境路径>"
    compose_file="${VULHUB_DIR}/${env_dir}/docker-compose.yml"
    [ -f "$compose_file" ] || [ -f "${VULHUB_DIR}/${env_dir}/compose.yaml" ] || die "环境不存在: ${env_dir}"
    compose -f "$compose_file" down --remove-orphans
    echo "vulhub: ${env_dir} 已关并清理"
    ;;
list)
    [ -f "${VULHUB_DIR}/.fetched" ] || { echo "未下载,先执行 lab/vulhub.sh fetch"; exit 0; }
    find "$VULHUB_DIR" -name docker-compose.yml -o -name compose.yaml \
        | sed "s|${VULHUB_DIR}/||; s|/docker-compose.yml||; s|/compose.yaml||" | sort
    ;;
clean)
    rm -rf "$VULHUB_DIR" "$CACHE_DIR"
    echo "vulhub: 已删本地仓库与缓存(运行中的环境先逐个 down)"
    ;;
*)
    die "用法: lab/vulhub.sh fetch|up <环境>|down <环境>|list|clean"
    ;;
esac
