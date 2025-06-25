package ioc

import (
	"github.com/ego-component/eetcd"
	"github.com/ego-component/eetcd/registry"
)

func InitRegistry() *registry.Component {
	return registry.Load("registry").Build(registry.WithClientEtcd(eetcd.Load("etcd").Build()))
}
