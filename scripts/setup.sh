#!/bin/sh

SOURCE_COMMIT=./scripts/git/pre-commit
TARGET_COMMIT=.git/hooks/pre-commit
SOURCE_PUSH=./scripts/git/pre-push
TARGET_PUSH=.git/hooks/pre-push

# copy pre-commit file if not exist.
echo "设置 git pre-commit hooks..."
cp $SOURCE_COMMIT $TARGET_COMMIT

# copy pre-push file if not exist.
echo "设置 git pre-push hooks..."
cp $SOURCE_PUSH $TARGET_PUSH

# add permission to TARGET_PUSH and TARGET_COMMIT file.
test -x $TARGET_PUSH || chmod +x $TARGET_PUSH
test -x $TARGET_COMMIT || chmod +x $TARGET_COMMIT

echo "安装 golangci-lint..."
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8

echo "安装 goimports..."
go install golang.org/x/tools/cmd/goimports@latest

echo "安装 mockgen..."
go install go.uber.org/mock/mockgen@latest

echo "安装 wire..."
go install github.com/google/wire/cmd/wire@latest

echo "安装 buf, protoc-gen-buf-breaking, protoc-gen-buf-lint......"
go install github.com/bufbuild/buf/cmd/buf@v1.50.1