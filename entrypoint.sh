#!/bin/sh
# entrypoint.sh — 解析运行身份 → 必要时对齐挂载目录属主 → su-exec 降权启动。
#
# 设计要点（见 docs/superpowers/specs/2026-09-21-docker-config-save-design.md）：
#   * 容器默认以 root 启动、解析身份后降权；app 进程始终非 root（降权后无 capabilities）。
#   * 仅当"顶层挂载目录属主 ≠ 目标身份"时才 chown -R（切换身份才触发），每次动作留日志痕。
#   * 配置固定落在目录挂载内（默认 /app/data/config.json）。单文件挂载会让
#     rename 覆盖挂载点失败（EBUSY），tmp+rename 的原子替换就不成立。
set -eu

CONFIG="${WB2A_CONFIG:-/app/data/config.json}"
export WB2A_CONFIG="$CONFIG"          # login.sh 等容器内脚本共用同一路径来源

# 不带命令时（docker run 未给 CMD）的默认命令：Dockerfile 已不设 CMD。
if [ "$#" -eq 0 ]; then set -- /app/wb2api -config "$CONFIG"; fi

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
# 避免把镜像内任何自带文件误当配置。
MIGRATED=""
if [ ! -e "$CONFIG" ] && [ -f /app/config.json ] &&
   [ "$(stat -c %d /app/config.json)" != "$(stat -c %d /app)" ]; then
  if cp /app/config.json "$CONFIG" 2>/dev/null; then
    MIGRATED=1
    echo "entrypoint: 已把旧 ./config.json 迁移到 ./data/config.json（旧文件不再被读取，确认后删除该挂载）" >&2
  else
    echo "WARN: 旧 ./config.json 存在但写不进 $CONFIG，请手工迁移（见 README「升级说明」）" >&2
  fi
fi

# PUID=0 = 显式要求以 root 运行（逃生门；root 可写一切，无需 chown）。
if [ "$uid" = "0" ]; then exec "$@"; fi

if [ "$(id -u)" = "0" ]; then
  if [ -n "$MIGRATED" ]; then chown "$uid:$gid" "$CONFIG"; fi
  for d in /app/auths /app/data; do
    [ -d "$d" ] || continue
    if [ "$(stat -c %u:%g "$d")" != "$uid:$gid" ]; then
      echo "entrypoint: chown -R $uid:$gid $d" >&2
      chown -R "$uid:$gid" "$d"
    fi
  done
  exec su-exec "$uid:$gid" "$@"
fi

# 已是非 root 启动（compose 里固定了 user:）：不 chown，只预检告警，不阻断启动。
for d in /app/auths /app/data; do
  [ -w "$d" ] || echo "WARN: $d 对 uid=$(id -u) 不可写，面板保存/落盘会失败，见 README「Docker 权限排障」" >&2
done
exec "$@"
