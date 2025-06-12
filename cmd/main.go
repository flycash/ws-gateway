package main

import (
	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/cmd/ioc"
	"github.com/ecodeclub/ekit/slice"
	"github.com/gotomicro/ego"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/server"
	"github.com/gotomicro/ego/server/egovernor"
)

// 运行要加上 --config=config/config.yaml
// 并且可以开启环境变量 EGO_DEBUG=true
func main() {
	app := ego.New()
	elog.DefaultLogger = elog.Load("log").Build()
	all := ioc.InitApp()
	servers := slice.Map(all.OrderServer, func(_ int, src gateway.Server) server.Server {
		return src
	})
	servers = append(servers, egovernor.Load("server.governor").Build())
	if err := app.Serve(servers...).Run(); err != nil {
		elog.Panic("startup", elog.FieldErr(err))
	}
}
