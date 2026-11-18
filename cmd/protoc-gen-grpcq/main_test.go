package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func TestGenerateFileUsesFireAndForgetProducerSignatures(t *testing.T) {
	plugin, err := protogen.Options{}.New(unaryRequest())
	if err != nil {
		t.Fatalf("protogen setup failed: %v", err)
	}
	if err := generate(plugin); err != nil {
		t.Fatal(err)
	}
	resp := plugin.Response()
	if len(resp.File) != 1 {
		t.Fatalf("expected one generated file, got %d", len(resp.File))
	}
	content := resp.File[0].GetContent()
	if !strings.Contains(content, "CreateUser(ctx context.Context, in *CreateRequest, opts ...grpcq.CallOption) error") {
		t.Fatalf("generated producer signature still looks synchronous:\n%s", content)
	}
	if strings.Contains(content, "out := new(") {
		t.Fatalf("generated producer still allocates a response:\n%s", content)
	}
}

func TestGenerateFileRejectsStreamingMethods(t *testing.T) {
	for _, tc := range []struct {
		name           string
		client, server bool
	}{
		{name: "client", client: true},
		{name: "server", server: true},
		{name: "bidirectional", client: true, server: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := unaryRequest()
			method := req.ProtoFile[0].Service[0].Method[0]
			method.ClientStreaming = &tc.client
			method.ServerStreaming = &tc.server
			plugin, err := protogen.Options{}.New(req)
			if err != nil {
				t.Fatal(err)
			}
			err = generate(plugin)
			if err == nil || !strings.Contains(err.Error(), "test.UserService/CreateUser") || !strings.Contains(err.Error(), "only supports unary") {
				t.Fatalf("streaming error lacks context: %v", err)
			}
			if len(plugin.Response().File) != 0 {
				t.Fatal("unsupported service produced misleading stubs")
			}
		})
	}
}

func TestGenerateFileWithoutServices(t *testing.T) {
	req := unaryRequest()
	req.ProtoFile[0].Service = nil
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := generate(plugin); err != nil {
		t.Fatal(err)
	}
	if len(plugin.Response().File) != 0 {
		t.Fatal("message-only file produced service stubs")
	}
}

func TestGenerateSupportsProto3OptionalRequests(t *testing.T) {
	req := unaryRequest()
	message := req.ProtoFile[0].MessageType[0]
	message.OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_name")}}
	message.Field = []*descriptorpb.FieldDescriptorProto{{
		Name: proto.String("name"), Number: proto.Int32(1),
		Type:       descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		OneofIndex: proto.Int32(0), Proto3Optional: proto.Bool(true),
	}}
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := generate(plugin); err != nil {
		t.Fatal(err)
	}
	response := plugin.Response()
	if response.GetError() != "" || len(response.File) != 1 {
		t.Fatalf("optional request generation failed: %v", response)
	}
	if response.GetSupportedFeatures()&uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL) == 0 {
		t.Fatal("plugin does not advertise support for proto3 optional fields")
	}
}

func TestGenerateSkipsImportedServices(t *testing.T) {
	req := unaryRequest()
	req.ProtoFile[0].Service[0].Method[0].ServerStreaming = proto.Bool(true)
	req.ProtoFile = append(req.ProtoFile, &descriptorpb.FileDescriptorProto{
		Name: proto.String("messages.proto"), Syntax: proto.String("proto3"),
		Package: proto.String("messages"), Dependency: []string{"test.proto"},
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("github.com/example/messages")},
	})
	req.FileToGenerate = []string{"messages.proto"}
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := generate(plugin); err != nil {
		t.Fatalf("imported streaming service should not be generated: %v", err)
	}
	if len(plugin.Response().File) != 0 {
		t.Fatal("imported service produced stubs")
	}
}

func unaryRequest() *pluginpb.CodeGeneratorRequest {
	return &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"test.proto"},
		ProtoFile: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("test.proto"),
				Package: proto.String("test"),
				Syntax:  proto.String("proto3"),
				Options: &descriptorpb.FileOptions{
					GoPackage: proto.String("github.com/example/testpb;testpb"),
				},
				MessageType: []*descriptorpb.DescriptorProto{
					{Name: proto.String("CreateRequest")},
					{Name: proto.String("CreateResponse")},
				},
				Service: []*descriptorpb.ServiceDescriptorProto{
					{
						Name: proto.String("UserService"),
						Method: []*descriptorpb.MethodDescriptorProto{
							{
								Name:       proto.String("CreateUser"),
								InputType:  proto.String(".test.CreateRequest"),
								OutputType: proto.String(".test.CreateResponse"),
							},
						},
					},
				},
			},
		},
	}
}
