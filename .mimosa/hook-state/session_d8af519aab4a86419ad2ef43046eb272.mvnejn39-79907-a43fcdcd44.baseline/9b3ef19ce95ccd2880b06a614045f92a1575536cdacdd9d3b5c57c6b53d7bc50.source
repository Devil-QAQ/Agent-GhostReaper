#!/bin/sh
# GhostReaper 守护脚本(二进制自保,P3-12)。
# 用法:ghostreaperd.sh [start] [-- ghostreaper 参数...]
#       ghostreaperd.sh upgrade <新二进制>
#   start    前台守护:拉起二进制,按退出码决定是否再拉(默认动作,可省)
#   upgrade  升级:当前版先备份为 <二进制>.old,再换新版落位;下次拉起即新版
#
# 退出码契约(ghostreaper 侧,main.go exitStartupFailed):
#   0 / 130 / 143  主动退出 / 操作者信号停止——不拉起,守护结束
#   78             起不来(坏配置 / 坏版本)——连续 N 次自动回滚
#                  <二进制>.old 旧版重拉;无旧版可回滚则停,不死循环
#   126 / 127      二进制不可执行 / 不存在——守护结束
#   其余           运行期崩溃——1 秒后拉起
#
# 环境变量:
#   GR_BIN             被守护的二进制(默认 脚本同目录 bin/ghostreaper)
#   GR_ROLLBACK_AFTER  连续启动失败回滚阈值(默认 3)
set -eu

SELF_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
GR_BIN="${GR_BIN:-${SELF_DIR}/bin/ghostreaper}"
OLD_BIN="${GR_BIN}.old"
ROLLBACK_AFTER="${GR_ROLLBACK_AFTER:-3}"

die() { echo "ghostreaperd: $*" >&2; exit 1; }
say() { echo "[ghostreaperd] $(date '+%Y-%m-%d %H:%M:%S') $*"; }

usage() {
	cat >&2 <<'EOF'
用法:ghostreaperd.sh [start] [-- ghostreaper 参数...]
      ghostreaperd.sh upgrade <新二进制>
  start    前台守护:拉起二进制,按退出码决定是否再拉(默认动作,可省)
  upgrade  升级:当前版先备份为 <二进制>.old,再换新版落位;下次拉起即新版
环境变量:
  GR_BIN             被守护的二进制(默认 脚本同目录 bin/ghostreaper)
  GR_ROLLBACK_AFTER  连续启动失败回滚阈值(默认 3)
EOF
	exit 2
}

# upgrade <新二进制>:备份当前版 → 新版落位(权限保持可执行)。
cmd_upgrade() {
	new="$1"
	[ -f "$new" ] || die "新二进制不存在: $new"
	[ -f "$GR_BIN" ] || die "当前二进制不存在: $GR_BIN(首次部署直接放 $GR_BIN)"
	cp -p "$GR_BIN" "$OLD_BIN"
	cp "$new" "${GR_BIN}.incoming"
	chmod +x "${GR_BIN}.incoming"
	mv -f "${GR_BIN}.incoming" "$GR_BIN"
	say "升级完成: 旧版备份为 $(basename "$OLD_BIN"),下次拉起即新版"
}

# start:守护主循环。参数原样传给二进制。
cmd_start() {
	[ -x "$GR_BIN" ] || die "二进制不存在或不可执行: $GR_BIN"
	fails=0
	say "守护开始: $GR_BIN"
	while :; do
		# 非零退出在 || 分支吸收,set -e 不打断守护循环。
		code=0
		"$GR_BIN" "$@" || code=$?
		case $code in
		0 | 130 | 143)
			say "退出码 $code(主动退出),守护结束"
			exit 0
			;;
		78)
			fails=$((fails + 1))
			if [ "$fails" -lt "$ROLLBACK_AFTER" ]; then
				say "启动失败 $fails/$ROLLBACK_AFTER(退出码 78),重试"
				sleep 1
				continue
			fi
			if [ -f "$OLD_BIN" ]; then
				say "连续 $ROLLBACK_AFTER 次启动失败,回滚旧版 $(basename "$OLD_BIN")"
				mv -f "$OLD_BIN" "$GR_BIN"
				fails=0
				continue
			fi
			say "连续 $ROLLBACK_AFTER 次启动失败且无旧版可回滚,守护结束"
			exit 1
			;;
		126 | 127)
			say "退出码 $code(二进制不可执行 / 不存在),守护结束"
			exit 1
			;;
		*)
			say "退出码 $code(运行期崩溃),1 秒后拉起"
			fails=0
			sleep 1
			;;
		esac
	done
}

if [ $# -eq 0 ]; then
	cmd_start
	exit 0
fi
case "$1" in
start)
	shift
	if [ "${1:-}" = "--" ]; then shift; fi
	cmd_start "$@"
	;;
upgrade)
	[ $# -eq 2 ] || usage
	cmd_upgrade "$2"
	;;
-h | --help)
	usage
	;;
*)
	# 裸参数直接当二进制参数守护:ghostreaperd.sh -addr 127.0.0.1:28282
	cmd_start "$@"
	;;
esac
