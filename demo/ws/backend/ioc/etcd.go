package ioc

import "github.com/ego-component/eetcd"

func InitEtcdClient() *eetcd.Component {
	return eetcd.Load("etcd").Build()
}
