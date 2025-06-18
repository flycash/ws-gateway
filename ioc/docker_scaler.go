package ioc

import (
	"context"
	"fmt"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/scaler"
	"github.com/docker/docker/client"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/econf"
)

// InitDockerClient 初始化Docker客户端
func InitDockerClient() *client.Client {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(err)
	}
	return cli
}

// InitDockerScaler 初始化Docker扩容器
func InitDockerScaler(
	dockerClient *client.Client,
	etcdClient *eetcd.Component,
	registry gateway.ServiceRegistry,
	nodeInfo *apiv1.Node,
) scaler.Scaler {
	// 镜像名称模式，用于查找最新的网关镜像
	imageNamePattern := "ws-gateway-*"

	// Docker Compose项目名称，用于生成网络名和容器标签
	dockerComposeProject := econf.GetString("docker.compose_project")
	if dockerComposeProject == "" {
		panic("docker compose 项目名不能为空（-p 参数）")
	}

	return scaler.NewDockerScaler(
		dockerClient,
		etcdClient,
		registry,
		nodeInfo.GetId(), // 使用当前节点ID
		fmt.Sprintf("%d", econf.GetInt("server.websocket.port")), // 转换为字符串
		func(_ context.Context) (string, error) {
			return "localhost", nil
		},
		imageNamePattern,
		dockerComposeProject,
	)
}
