#!/bin/sh
# entrypoint.sh — 解析运行身份 → 必要时对齐挂载目录属主 → su-exec 降权启动。
#
# 设计要点（见 docs/superpowers/specs/2026-09-21-docker-config-save-design.md）：
#   * 容器默认以 root 启动、解析身份后降权；app 进程始终非 root（降权后无 capabilities）。
#   * 仅当"顶层挂载目录/配置文件属主"与目标身份不符时才 chown，每次动作留日志痕。
#   * 配置固定落在目录挂载内（默认 /app/data/config.json）。单文件挂载会让
#     rename 覆盖挂载点失败（EBUSY），tmp+rename 的原子替换就不成立。
set -eu

CONFIG="${WB2A_CONFIG:-/app/data/config.json}"
export WB2A_CONFIG="$CONFIG"          # login.sh 等容器内脚本共用同一路径来源

# 命令解析：无参 → 默认命令；首参形如 -xxx → 当作二进制的参数追加到默认命令后。
# （旧 ENTRYPOINT 自带 -config，`docker run <img> --listen :8080` 本来是能用的，
#   换成脚本后不能让它变成 su-exec 的执行目标。）
if [ "$#" -eq 0 ]; then
  set -- /app/wb2api -config "$CONFIG"
elif [ "${1#-}" != "$1" ]; then
  set -- /app/wb2api -config "$CONFIG" "$@"
fi

DATA=/app/data
owner=$(stat -c %u "$DATA" 2>/dev/null || echo 10001)
group=$(stat -c %g "$DATA" 2>/dev/null || echo 10001)
# Docker 在宿主目录缺失时会以 root 建目录 → 整对回落到镜像设计身份，随后 chown 给它。
if [ "$owner" = "0" ]; then owner=10001; group=10001; fi

uid="${PUID:-$owner}"
gid="${PGID:-${PUID:-$group}}"        # 给了 PUID 未给 PGID → 跟随 uid；否则用目录组
case "$uid$gid" in                    # 环境变量是手填输入：非数字回落自动适配，别让 su-exec 抛裸错误
  *[!0-9]*)
    echo "WARN: PUID/PGID 非数字（'${PUID:-}'/'${PGID:-}'），已回落自动适配" >&2
    uid="$owner"; gid="$group"
    ;;
esac

# 迁移期兼容：放在所有分支之前（四种启动方式都要自愈；cp 本身不需要权限）。
# 仅当目标缺失、且旧路径确实是被单独挂载的文件（st_dev 与 /app 不同）时才当作旧配置——
# 避免把镜像内任何自带文件误当配置。用 tmp + mv 保证原子：中途被 kill 不会留下截断的
# 配置（截断文件会让下次启动判为"已存在"而永不重试，程序读到非法 JSON 进重启循环）。
MIGRATED=""
if [ ! -e "$CONFIG" ] && [ -f /app/config.json ] &&
   [ "$(stat -c %d /app/config.json)" != "$(stat -c %d /app)" ]; then
  if cp /app/config.json "$CONFIG.tmp" 2>/dev/null && mv "$CONFIG.tmp" "$CONFIG" 2>/dev/null; then
    MIGRATED=1
    echo "entrypoint: 已把旧 ./config.json 迁移到 ./data/config.json（旧文件不再被读取，确认后删除该挂载）" >&2
  else
    rm -f "$CONFIG.tmp" 2>/dev/null || true
    echo "WARN: 旧 ./config.json 存在但写不进 $CONFIG（配置目录对当前身份不可写？）。" >&2
    echo "WARN: 后果：本次将以空 api_key 启动 = **不鉴权**（7863 已对外映射），且旧配置不会被读取。" >&2
    echo "WARN: 请立即按 README「升级说明」迁移，或先按「Docker 权限排障」对齐目录属主。" >&2
  fi
fi

# PUID=0 = 显式要求以 root 运行（逃生门；root 可写一切，无需 chown）。
# 用 -eq 数字比较：`PUID=00` 也应当算 root，字符串比较会让它绕过这条逃生门。
if [ "$uid" -eq 0 ]; then exec "$@"; fi

if [ "$(id -u)" = "0" ]; then
  if [ -n "$MIGRATED" ]; then chown "$uid:$gid" "$CONFIG"; fi
  # 属主对齐分两层：目录用 -R；配置文件本身单独修。顶层目录属主正确 ≠ 文件属主正确——
  # 典型现场：曾以 PUID=0 跑过一轮（跳过 chown）写下 root:0600 的 config.json，
  # 之后切回普通身份时目录"匹配"→ 不 chown → Load 读不到 → 无限重启。
  # 用 -ne 数字比较，避免 PUID=01000 这类前导零导致的"永不相等 → 每次启动都 chown"。
  for d in /app/auths /app/data; do
    [ -d "$d" ] || continue
    if [ "$(stat -c %u "$d")" -ne "$uid" ] || [ "$(stat -c %g "$d")" -ne "$gid" ]; then
      echo "entrypoint: chown -R $uid:$gid $d" >&2
      chown -R "$uid:$gid" "$d"
    fi
  done
  if [ -e "$CONFIG" ] &&
     { [ "$(stat -c %u "$CONFIG")" -ne "$uid" ] || [ "$(stat -c %g "$CONFIG")" -ne "$gid" ]; }; then
    echo "entrypoint: chown $uid:$gid $CONFIG" >&2
    chown "$uid:$gid" "$CONFIG"
  fi
  exec su-exec "$uid:$gid" "$@"
fi

# 已是非 root 启动（compose 里固定了 user:）：不 chown，只预检告警，不阻断启动。
for d in /app/auths /app/data; do
  [ -w "$d" ] || echo "WARN: $d 对 uid=$(id -u) 不可写，面板保存/落盘会失败，见 README「Docker 权限排障」" >&2
done
if [ ! -r "$CONFIG" ]; then
  echo "WARN: 配置 $CONFIG 不存在或不可读：程序会尝试自动生成；若目录不可写则回落空 api_key" >&2
  echo "WARN: = **不鉴权**（7863 对外映射）。见 README「升级说明」/「Docker 权限排障」。" >&2
fi
exec "$@"
