# 构建：node build.mjs 生成静态页面（无依赖）
FROM node:22-alpine AS build
WORKDIR /src
COPY . .
# 价格页从这个地址读取套餐（GET /v1/plans），同时写进 CSP 的 connect-src
ARG API_URL=https://nextreply-api.novakitestudio.com
RUN node build.mjs --api "$API_URL" && rm -f dist/_headers

# 运行：nginx 提供静态文件；安全响应头见 nginx.conf.template（与 Cloudflare Pages 的 static/_headers 一致）
FROM nginx:1.27-alpine
ARG API_URL=https://nextreply-api.novakitestudio.com
# 官方镜像启动时用 envsubst 渲染 /etc/nginx/templates/*.template，只替换已定义的环境变量（$uri 等不受影响）
ENV API_URL=${API_URL}
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1/ || exit 1
