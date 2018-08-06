// Code generated by protoc-gen-goclay, but your can (must) modify it.
// source: pb/strings.proto

package strings

import (
	"context"

	desc "github.com/utrack/clay/integration/binding_with_body_and_response/pb"
	transport "github.com/utrack/clay/v2/transport"
	"strings"
)

type StringsImplementation struct{}

func NewStrings() *StringsImplementation {
	return &StringsImplementation{}
}

func (i *StringsImplementation) ToUpper(ctx context.Context, req *desc.String) (rsp *desc.String, err error) {
	rsp = &desc.String{}
	for _, str := range req.Str {
		rsp.Str = append(rsp.Str, strings.ToUpper(str))
	}
	return
}

// GetDescription is a simple alias to the ServiceDesc constructor.
// It makes it possible to register the service implementation @ the server.
func (i *StringsImplementation) GetDescription() transport.ServiceDesc {
	return desc.NewStringsServiceDesc(i)
}