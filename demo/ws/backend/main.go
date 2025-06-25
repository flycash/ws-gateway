package main

import (
	"gitee.com/flycash/ws-gateway/demo/ws/backend/ioc"
	"github.com/gotomicro/ego"
)

// 运行要加上 --config=config/config.yaml
// 并且可以开启环境变量 EGO_DEBUG=true
func main() {
	ego.New()
	app := ioc.InitGrpcServer()
	if err := app.Run(); err != nil {
		panic(err)
	}
}
