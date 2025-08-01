### helloworld案例 

1. 创建领域目录,在`api/proto/`下创建`helloworld`及`helloworld/v1`目录
2. 编写PB文件,编写`api/proto/helloworld/v1/helloworld.proto`文件

```protobuf
syntax = "proto3";

package helloworld.v1;

// 生成的helloworld.pb.go及helloworld_grpc.pb.go文件内的包名即Go语言领域的包概念
option go_package = "helloworld/v1;helloworldv1";

// 定义一个 HelloRequest 消息，用于客户端向服务器发送请求
message SayHelloRequest {
  string name = 1;
}

// 定义一个 SayHelloResponse 消息，用于服务器向客户端发送响应
message SayHelloResponse {
  string message = 1;
}

// 定义一个 GreeterService 服务，包含一个 SayHello 方法
service GreeterService {
  // SayHello 方法接受 HelloRequest 消息，并返回 SayHelloResponse 消息
  rpc SayHello (SayHelloRequest) returns (SayHelloResponse);
}
```
3. 在**项目的根目录**下执行命令`make grpc`
4. 每当在`buf.yaml`文件中添加新`deps`后,在`api/proto`目录中执行`buf mod update`来更新`buf.lock`文件