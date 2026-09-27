FROM node:22-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install
COPY frontend/ ./
RUN npm run build

FROM golang:1.22-alpine AS backend-builder
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/one-search ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl nginx postgresql postgresql-client su-exec tzdata
WORKDIR /app
COPY --from=frontend-builder /app/frontend/dist /usr/share/nginx/html
COPY --from=backend-builder /out/one-search /usr/local/bin/one-search
COPY backend/migrations /app/backend/migrations
# nginx 站点配置以模板形式安装：入口脚本在启动 nginx 前把其中的 __XXX__
# 占位符替换成环境变量值（nginx 自身不读环境变量，见 nginx.conf.template 头部说明）
COPY deploy/nginx.conf.template /etc/nginx/nginx.conf.template
COPY deploy/all-in-one-entrypoint.sh /usr/local/bin/all-in-one-entrypoint.sh
RUN chmod +x /usr/local/bin/all-in-one-entrypoint.sh
# 渲染 nginx 配置用的最小包装文件；构建期先删掉镜像自带的站点配置，
# 避免未经渲染的旧配置被 /etc/nginx/http.d/*.conf 一同 include
RUN printf 'events {}\nhttp {\n  include /etc/nginx/http.d/default.conf;\n}\n' > /etc/nginx/nginx-test.conf \
    && rm -f /etc/nginx/http.d/default.conf
EXPOSE 80
VOLUME ["/var/lib/postgresql/data"]
ENTRYPOINT ["/usr/local/bin/all-in-one-entrypoint.sh"]
