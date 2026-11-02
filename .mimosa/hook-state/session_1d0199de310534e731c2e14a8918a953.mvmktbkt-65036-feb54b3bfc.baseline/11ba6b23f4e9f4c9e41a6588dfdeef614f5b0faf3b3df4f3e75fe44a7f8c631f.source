#!/bin/sh
# GhostReaper 本地靶场 · DVWA 主力(一个容器一个网站,稳定地址)。
# 用法:lab/dvwa.sh up|down|status|reset|clean
#   up     一条命令起:缺镜像先构建 → 起容器 → 等 HTTP 就绪 → 初始化
#          (登录 admin/password → setup.php 建库表 → 安全等级 low)→ 报地址
#   down   停容器(容器保留,下次 up 秒起,数据保留)
#   status 容器状态 + 站点可达 + 当前脚本会话安全等级
#   reset  删容器重建(库表重置,数据全清)
#   clean  删容器 + 删镜像(收尾清理用)
#
# 地址与凭据(标准授权目标,登记见 lab/README.md):
#   URL      http://127.0.0.1:4280
#   账号     admin / password
#   安全等级 默认 low;登录后在 DVWA 页面可改 low/medium/high/impossible
set -eu

LAB_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
IMAGE="ghostreaper-dvwa:latest"
CONTAINER="ghostreaper-dvwa"
HOST_PORT=4280
BASE_URL="http://127.0.0.1:${HOST_PORT}"
COOKIE_JAR="${TMPDIR:-/tmp}/ghostreaper-dvwa-cookies.txt"

die() { echo "dvwa: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "缺依赖 $1"; }
need docker
need curl

docker_running() { docker info >/dev/null 2>&1; }

container_state() {
    docker inspect -f '{{.State.Status}}' "$CONTAINER" 2>/dev/null || echo "absent"
}

build_image() {
    echo "dvwa: 构建镜像 ${IMAGE}(首次构建需下载依赖,耐心等待)"
    # 本机 BuildKit 直连 registry 超时(仅 daemon 拉取走代理),故先经
    # daemon 预拉基底镜像,再用经典构建器(用本地镜像,不解析远端元数据);
    # Docker 移除经典构建器后,须先解决 BuildKit 的 registry 代理问题。
    docker pull php:8-apache
    docker pull composer:2
    DOCKER_BUILDKIT=0 docker build -t "$IMAGE" "$LAB_DIR/dvwa"
}

wait_http() {
    echo "dvwa: 等待站点就绪 ${BASE_URL}"
    i=0
    while ! curl -sf -m 3 -o /dev/null "${BASE_URL}/login.php"; do
        i=$((i + 1))
        [ "$i" -ge 60 ] && {
            docker logs "$CONTAINER" 2>&1 | tail -20 || true
            die "60 秒未就绪(上面是容器日志)"
        }
        sleep 1
    done
}

# 页面里抓 anti-CSRF token(master 分支表单带 user_token;抓不到就不带)
page_token() { grep -o "name='user_token' value='[a-f0-9]*'" | sed "s/.*value='\([a-f0-9]*\)'.*/\1/" | head -1; }

dvwa_login() {
    rm -f "$COOKIE_JAR"
    token=$(curl -sf -c "$COOKIE_JAR" "${BASE_URL}/login.php" | page_token || true)
    code=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
        --data-urlencode "username=admin" \
        --data-urlencode "password=password" \
        --data-urlencode "Login=Login" \
        ${token:+"--data-urlencode"} ${token:+"user_token=${token}"} \
        "${BASE_URL}/login.php")
    # 登录成功 302 跳 index;200 即凭据/库问题
    [ "$code" = "302" ] || [ "$code" = "200" ] || die "登录失败(HTTP $code)"
}

dvwa_init() {
    echo "dvwa: 初始化(建库表 → 登录 → 安全等级 low)"
    # 首步必须 setup.php:库表与 admin 用户由此创建(官方首次使用流程)
    rm -f "$COOKIE_JAR"
    token=$(curl -sf -c "$COOKIE_JAR" "${BASE_URL}/setup.php" | page_token || true)
    # setup 成功以 302 跳转呈现,-L 跟随到结果页
    if [ -n "$token" ]; then
        out=$(curl -sfL -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
            --data-urlencode "create_db=Create / Reset Database" \
            --data-urlencode "user_token=${token}" \
            "${BASE_URL}/setup.php")
    else
        out=$(curl -sfL -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
            --data-urlencode "create_db=Create / Reset Database" \
            "${BASE_URL}/setup.php")
    fi
    echo "$out" | grep -Eq "has been (created|reset)|already exist" \
        || die "setup.php 初始化失败(输出片段:$(echo "$out" | grep -i -m1 -E 'database|error' || echo 无))"
    dvwa_login
    curl -sfL -b "$COOKIE_JAR" "${BASE_URL}/index.php" | grep -q "DVWA" || {
        docker logs "$CONTAINER" 2>&1 | tail -20 || true
        die "登录后 index.php 未出现 DVWA 字样(凭据或库异常,看容器日志)"
    }
    # security.php:本会话安全等级 low(镜像默认已是 low,双保险并回显实证)
    token=$(curl -sf -b "$COOKIE_JAR" -c "$COOKIE_JAR" "${BASE_URL}/security.php" | page_token || true)
    if [ -n "$token" ]; then
        out=$(curl -sfL -b "$COOKIE_JAR" \
            --data-urlencode "security=low" \
            --data-urlencode "seclev_submit=Submit" \
            --data-urlencode "user_token=${token}" \
            "${BASE_URL}/security.php")
    else
        out=$(curl -sfL -b "$COOKIE_JAR" \
            --data-urlencode "security=low" \
            --data-urlencode "seclev_submit=Submit" \
            "${BASE_URL}/security.php")
    fi
    echo "$out" | grep -q "Security level set to low" || die "安全等级设置失败"
    echo "dvwa: 初始化完成"
}

cmd=${1:-up}
case "$cmd" in
up)
    docker_running || die "Docker 未运行"
    docker image inspect "$IMAGE" >/dev/null 2>&1 || build_image
    state=$(container_state)
    if [ "$state" = "absent" ]; then
        echo "dvwa: 起容器 ${CONTAINER}(${BASE_URL} → 容器 80)"
        docker run -d --name "$CONTAINER" \
            -p "127.0.0.1:${HOST_PORT}:80" \
            "$IMAGE" >/dev/null
    elif [ "$state" != "running" ]; then
        docker start "$CONTAINER" >/dev/null
    fi
    wait_http
    dvwa_init
    echo "dvwa: 就绪 ${BASE_URL}(admin/password,安全等级 low)"
    ;;
down)
    [ "$(container_state)" = "absent" ] && { echo "dvwa: 无容器"; exit 0; }
    docker stop "$CONTAINER" >/dev/null
    echo "dvwa: 已停(容器保留,lab/dvwa.sh up 秒起)"
    ;;
status)
    echo "容器: $(container_state)"
    if curl -sf -m 3 -o /dev/null "${BASE_URL}/login.php"; then
        echo "站点: ${BASE_URL} 可达"
        dvwa_login >/dev/null
        curl -sf -b "$COOKIE_JAR" "${BASE_URL}/security.php" \
            | grep -o "Security level is currently: <em>[a-z]*</em>" || true
    else
        echo "站点: ${BASE_URL} 不可达"
    fi
    ;;
reset)
    [ "$(container_state)" = "absent" ] || docker rm -f "$CONTAINER" >/dev/null
    echo "dvwa: 容器已删,重建中(库表重置)"
    exec "$0" up
    ;;
clean)
    [ "$(container_state)" = "absent" ] || docker rm -f "$CONTAINER" >/dev/null
    docker image inspect "$IMAGE" >/dev/null 2>&1 && docker rmi "$IMAGE" >/dev/null
    echo "dvwa: 容器与镜像已删"
    ;;
*)
    die "未知子命令 $1(可用: up down status reset clean)"
    ;;
esac
