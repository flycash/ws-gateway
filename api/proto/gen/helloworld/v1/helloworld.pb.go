// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        (unknown)
// source: helloworld/v1/helloworld.proto

package helloworldv1

import (
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"

	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// 定义一个 HelloRequest 消息，用于客户端向服务器发送请求
type SayHelloRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Name          string                 `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SayHelloRequest) Reset() {
	*x = SayHelloRequest{}
	mi := &file_helloworld_v1_helloworld_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SayHelloRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SayHelloRequest) ProtoMessage() {}

func (x *SayHelloRequest) ProtoReflect() protoreflect.Message {
	mi := &file_helloworld_v1_helloworld_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SayHelloRequest.ProtoReflect.Descriptor instead.
func (*SayHelloRequest) Descriptor() ([]byte, []int) {
	return file_helloworld_v1_helloworld_proto_rawDescGZIP(), []int{0}
}

func (x *SayHelloRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

// 定义一个 SayHelloResponse 消息，用于服务器向客户端发送响应
type SayHelloResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Message       string                 `protobuf:"bytes,1,opt,name=message,proto3" json:"message,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SayHelloResponse) Reset() {
	*x = SayHelloResponse{}
	mi := &file_helloworld_v1_helloworld_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SayHelloResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SayHelloResponse) ProtoMessage() {}

func (x *SayHelloResponse) ProtoReflect() protoreflect.Message {
	mi := &file_helloworld_v1_helloworld_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SayHelloResponse.ProtoReflect.Descriptor instead.
func (*SayHelloResponse) Descriptor() ([]byte, []int) {
	return file_helloworld_v1_helloworld_proto_rawDescGZIP(), []int{1}
}

func (x *SayHelloResponse) GetMessage() string {
	if x != nil {
		return x.Message
	}
	return ""
}

var File_helloworld_v1_helloworld_proto protoreflect.FileDescriptor

const file_helloworld_v1_helloworld_proto_rawDesc = "" +
	"\n" +
	"\x1ehelloworld/v1/helloworld.proto\x12\rhelloworld.v1\"%\n" +
	"\x0fSayHelloRequest\x12\x12\n" +
	"\x04name\x18\x01 \x01(\tR\x04name\",\n" +
	"\x10SayHelloResponse\x12\x18\n" +
	"\amessage\x18\x01 \x01(\tR\amessage2]\n" +
	"\x0eGreeterService\x12K\n" +
	"\bSayHello\x12\x1e.helloworld.v1.SayHelloRequest\x1a\x1f.helloworld.v1.SayHelloResponseB\xc9\x01\n" +
	"\x11com.helloworld.v1B\x0fHelloworldProtoP\x01ZNgitee.com/flycash/permission-platform/api/proto/gen/helloworld/v1;helloworldv1\xa2\x02\x03HXX\xaa\x02\rHelloworld.V1\xca\x02\rHelloworld\\V1\xe2\x02\x19Helloworld\\V1\\GPBMetadata\xea\x02\x0eHelloworld::V1b\x06proto3"

var (
	file_helloworld_v1_helloworld_proto_rawDescOnce sync.Once
	file_helloworld_v1_helloworld_proto_rawDescData []byte
)

func file_helloworld_v1_helloworld_proto_rawDescGZIP() []byte {
	file_helloworld_v1_helloworld_proto_rawDescOnce.Do(func() {
		file_helloworld_v1_helloworld_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_helloworld_v1_helloworld_proto_rawDesc), len(file_helloworld_v1_helloworld_proto_rawDesc)))
	})
	return file_helloworld_v1_helloworld_proto_rawDescData
}

var (
	file_helloworld_v1_helloworld_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
	file_helloworld_v1_helloworld_proto_goTypes  = []any{
		(*SayHelloRequest)(nil),  // 0: helloworld.v1.SayHelloRequest
		(*SayHelloResponse)(nil), // 1: helloworld.v1.SayHelloResponse
	}
)

var file_helloworld_v1_helloworld_proto_depIdxs = []int32{
	0, // 0: helloworld.v1.GreeterService.SayHello:input_type -> helloworld.v1.SayHelloRequest
	1, // 1: helloworld.v1.GreeterService.SayHello:output_type -> helloworld.v1.SayHelloResponse
	1, // [1:2] is the sub-list for method output_type
	0, // [0:1] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_helloworld_v1_helloworld_proto_init() }
func file_helloworld_v1_helloworld_proto_init() {
	if File_helloworld_v1_helloworld_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_helloworld_v1_helloworld_proto_rawDesc), len(file_helloworld_v1_helloworld_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_helloworld_v1_helloworld_proto_goTypes,
		DependencyIndexes: file_helloworld_v1_helloworld_proto_depIdxs,
		MessageInfos:      file_helloworld_v1_helloworld_proto_msgTypes,
	}.Build()
	File_helloworld_v1_helloworld_proto = out.File
	file_helloworld_v1_helloworld_proto_goTypes = nil
	file_helloworld_v1_helloworld_proto_depIdxs = nil
}
