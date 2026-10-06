# 构建：纯 Go（CGO_ENABLED=0，SQLite 用 modernc.org/sqlite），产出静态二进制
FROM golang:1.27-alpine AS build
# 国内服务器可以在构建时传 --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/nextreply-server ./cmd/nextreply-server \
 && mkdir -p /out/data

# 运行：distroless（无 shell、非 root 用户 65532）
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/nextreply-server /nextreply-server
# 预先建好数据目录并交给非 root 用户，命名卷首次挂载时会继承这个属主
COPY --from=build --chown=65532:65532 /out/data /data
ENV LISTEN_ADDR=:8787 DATABASE_PATH=/data/nextreply.db
EXPOSE 8787
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/nextreply-server", "healthcheck"]
ENTRYPOINT ["/nextreply-server"]
CMD ["serve"]
