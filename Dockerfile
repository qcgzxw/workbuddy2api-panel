# syntax=docker/dockerfile:1
# builder 跟随上游（吸收 3b30809）：镜像内跑的是 CGO_ENABLED=0 静态二进制，
# 标准库漏洞会**编进**最终二进制，builder 版本即是运行期 stdlib 版本——
# 只升级基础镜像（alpine）不解决这条。go.mod 的 go 指令（1.22.5）是语言特性下限，
# 与 builder 版本无冲突（新工具链编译旧指令模块是支持的组合）。
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# 一次编译全部二进制（工具进镜像，容器内可直接跑脚本）。全部 -trimpath -s -w。
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wb2api ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/signin_bin ./cmd/signin \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/login ./cmd/login \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/credit ./cmd/credit

FROM alpine:3.20
# python3：login.sh 的 JSON 解析 / 签到 / 落盘；bash：shell 脚本体；
# su-exec：入口脚本降权用（镜像以 root 启动 → 解析身份 → 降权，见 entrypoint.sh）。
RUN apk add --no-cache wget ca-certificates tzdata python3 bash su-exec \
 && adduser -D -u 10001 app \
 && mkdir -p /app/auths /app/data \
 && chown -R app:app /app
WORKDIR /app
# 脚本置入 + 去 CRLF（Windows 检出可能性）在构建期的 root 下完成——
# 文件属主是 root，非 root 的 sed -i 会因「无目录写权限」失败。
COPY --from=build /out/wb2api /app/wb2api
COPY --from=build /out/signin_bin /app/signin_bin
COPY --from=build /out/login /app/login
COPY --from=build /out/credit /app/credit
COPY login.sh signin.sh credit.sh /app/
COPY scripts/probe_active.py /app/scripts/probe_active.py
RUN sed -i 's/\r$//' /app/login.sh /app/signin.sh /app/credit.sh && chmod 755 /app/login.sh /app/signin.sh /app/credit.sh
# 入口脚本放在 /app 之外：/app 会在运行期被 chown 给运行身份，脚本留在这里就能被
# 运行期进程替换，下次 root 启动即成提权链。
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN sed -i 's/\r$//' /usr/local/bin/entrypoint.sh && chmod 755 /usr/local/bin/entrypoint.sh
# 镜像不带任何配置文件：首次启动由程序自动生成随机 api_key 到 /app/data/config.json
# （此前内置 config.example.json 会把公开的 api_key=test_key 顶成"真实配置"，
#   还会让自动生成路径永远不触发——见 spec §1.5）
EXPOSE 7863
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:7863/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]