# 阶段1: 构建应用程序和依赖项
FROM golang:1.24 AS builder

# 设置容器内的工作目录
WORKDIR /app

# 复制Go模块文件
COPY go.mod go.sum ./

# 下载Go依赖项
RUN go env -w GOPROXY=https://goproxy.cn,direct
RUN go mod download && go mod tidy

# 复制其余的应用程序源代码
COPY . .

# 构建Go二进制文件
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o gateway cmd/main.go

# 阶段2: 创建用于执行的最小Docker镜像
FROM golang:1.24-alpine

# 设置容器内的工作目录
WORKDIR /app

# 从构建阶段复制已构建的gateway二进制文件
COPY --from=builder /app/gateway .

# 为gateway复制生产配置文件
COPY --from=builder /app/config/config.docker.yaml config.yaml

# 为gateway二进制文件设置可执行权限
RUN chmod +x ./gateway

ENV EGO_DEBUG=true

# 设置容器启动时运行的命令
CMD ["./gateway", "--config=config.yaml"]