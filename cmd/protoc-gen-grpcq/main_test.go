package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func TestGenerateFileUsesFireAndForgetProducerSignatures(t *testing.T) {
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"test.proto"},
		ProtoFile: []*descriptorpb.FileDescriptorProto{
			{
				Name:    stringPtr("test.proto"),
				Package: stringPtr("test"),
				Syntax:  stringPtr("proto3"),
				Options: &descriptorpb.FileOptions{
					GoPackage: stringPtr("github.com/example/testpb;testpb"),
				},
				MessageType: []*descriptorpb.DescriptorProto{
					{Name: stringPtr("CreateRequest")},
					{Name: stringPtr("CreateResponse")},
				},
				Service: []*descriptorpb.ServiceDescriptorProto{
					{
						Name: stringPtr("UserService"),
						Method: []*descriptorpb.MethodDescriptorProto{
							{
								Name:       stringPtr("CreateUser"),
								InputType:  stringPtr(".test.CreateRequest"),
								OutputType: stringPtr(".test.CreateResponse"),
							},
						},
					},
				},
			},
		},
	}

	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen setup failed: %v", err)
	}

	generateFile(plugin, plugin.Files[0])

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

func stringPtr(v string) *string {
	return &v
}
