package main

import (
	"log"
	"os"
	"strconv"
	"syscall"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/cmd/ioc"
	"github.com/ecodeclub/ekit/slice"
	"github.com/gotomicro/ego"
	"github.com/gotomicro/ego/core/econf"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/server"
	"github.com/gotomicro/ego/server/egovernor"
)

const (
	DefaultStopTimeout = 30 * time.Second
)

// 运行要加上 --config=config/config.yaml
// 并且可以开启环境变量 EGO_DEBUG=true
func main() {
	stopTimeout := DefaultStopTimeout
	n, err := strconv.ParseInt(os.Getenv("GATEWAY_STOP_TIMEOUT"), 10, 64)
	if err == nil {
		stopTimeout = time.Duration(n) * time.Second
	}
	app := ego.New(
		ego.WithStopTimeout(stopTimeout),
		ego.WithShutdownSignal(syscall.SIGINT, syscall.SIGTERM),
	)
	elog.DefaultLogger = elog.Load("log").Build()
	nodeInfo := &apiv1.Node{
		Id:       os.Getenv("GATEWAY_NODE_ID"),
		Ip:       getHost(),
		Port:     getPort(),
		Weight:   int32(econf.GetInt("server.websocket.weight")),
		Location: os.Getenv("GATEWAY_NODE_LOCATION"),
		Labels:   econf.GetStringSlice("server.websocket.labels"),
		Capacity: econf.GetInt64("server.websocket.capacity"),
		Load:     0,
	}
	log.Printf("nodeInfo = %#v\n", nodeInfo.String())
	all := ioc.InitApp(nodeInfo)
	servers := slice.Map(all.OrderServer, func(_ int, src gateway.Server) server.Server {
		return src
	})
	servers = append(servers, egovernor.Load("server.governor").Build())
	if err := app.Serve(servers...).Run(); err != nil {
		elog.Panic("startup", elog.FieldErr(err))
	}
}

func getHost() string {
	ip := os.Getenv("GATEWAY_NODE_IP")
	if ip != "" {
		return ip
	}
	return econf.GetString("server.websocket.host")
}

func getPort() int32 {
	port := os.Getenv("GATEWAY_NODE_PORT")
	atoi, err := strconv.ParseInt(port, 10, 32)
	if err == nil {
		return int32(atoi)
	}
	return int32(econf.GetInt("server.websocket.port"))
}
