// Code generated by protoc-gen-go. DO NOT EDIT.
// source: lnclipb/lncli.proto

package lnclipb

import (
	fmt "fmt"
	proto "github.com/golang/protobuf/proto"
	verrpc "github.com/pkt-cash/pktd/lnd/lnrpc/verrpc"
	math "math"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.ProtoPackageIsVersion3 // please upgrade the proto package

type VersionResponse struct {
	// The version information for pldctl.
	Pldctl *verrpc.Version `protobuf:"bytes,1,opt,name=pldctl,proto3" json:"pldctl,omitempty"`
	// The version information for pld.
	Pld                  *verrpc.Version `protobuf:"bytes,2,opt,name=pld,proto3" json:"pld,omitempty"`
	XXX_NoUnkeyedLiteral struct{}        `json:"-"`
	XXX_unrecognized     []byte          `json:"-"`
	XXX_sizecache        int32           `json:"-"`
}

func (m *VersionResponse) Reset()         { *m = VersionResponse{} }
func (m *VersionResponse) String() string { return proto.CompactTextString(m) }
func (*VersionResponse) ProtoMessage()    {}
func (*VersionResponse) Descriptor() ([]byte, []int) {
	return fileDescriptor_88b54c9c61b986c4, []int{0}
}

func (m *VersionResponse) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_VersionResponse.Unmarshal(m, b)
}
func (m *VersionResponse) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_VersionResponse.Marshal(b, m, deterministic)
}
func (m *VersionResponse) XXX_Merge(src proto.Message) {
	xxx_messageInfo_VersionResponse.Merge(m, src)
}
func (m *VersionResponse) XXX_Size() int {
	return xxx_messageInfo_VersionResponse.Size(m)
}
func (m *VersionResponse) XXX_DiscardUnknown() {
	xxx_messageInfo_VersionResponse.DiscardUnknown(m)
}

var xxx_messageInfo_VersionResponse proto.InternalMessageInfo

func (m *VersionResponse) GetPldctl() *verrpc.Version {
	if m != nil {
		return m.Pldctl
	}
	return nil
}

func (m *VersionResponse) GetPld() *verrpc.Version {
	if m != nil {
		return m.Pld
	}
	return nil
}

func init() {
	proto.RegisterType((*VersionResponse)(nil), "lnclipb.VersionResponse")
}

func init() { proto.RegisterFile("lnclipb/lncli.proto", fileDescriptor_88b54c9c61b986c4) }

var fileDescriptor_88b54c9c61b986c4 = []byte{
	// 155 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0x12, 0xce, 0xc9, 0x4b, 0xce,
	0xc9, 0x2c, 0x48, 0xd2, 0x07, 0xd3, 0x7a, 0x05, 0x45, 0xf9, 0x25, 0xf9, 0x42, 0xec, 0x50, 0x41,
	0x29, 0xe1, 0xb2, 0xd4, 0xa2, 0xa2, 0x82, 0x64, 0x7d, 0x08, 0x05, 0x91, 0x55, 0x8a, 0xe5, 0xe2,
	0x0f, 0x4b, 0x2d, 0x2a, 0xce, 0xcc, 0xcf, 0x0b, 0x4a, 0x2d, 0x2e, 0xc8, 0xcf, 0x2b, 0x4e, 0x15,
	0x52, 0xe7, 0x62, 0x2b, 0xc8, 0x49, 0x49, 0x2e, 0xc9, 0x91, 0x60, 0x54, 0x60, 0xd4, 0xe0, 0x36,
	0xe2, 0xd7, 0x83, 0xea, 0x80, 0x29, 0x84, 0x4a, 0x0b, 0x29, 0x72, 0x31, 0x17, 0xe4, 0xa4, 0x48,
	0x30, 0x61, 0x57, 0x05, 0x92, 0x73, 0xd2, 0x89, 0xd2, 0x4a, 0xcf, 0x2c, 0xc9, 0x28, 0x4d, 0xd2,
	0x4b, 0xce, 0xcf, 0xd5, 0x2f, 0xc8, 0x2e, 0xd1, 0x4d, 0x4e, 0x2c, 0xce, 0x00, 0x31, 0x52, 0xf4,
	0x73, 0xf2, 0x40, 0x18, 0xe4, 0x22, 0xa8, 0x0b, 0x93, 0xd8, 0xc0, 0x6e, 0x32, 0x06, 0x04, 0x00,
	0x00, 0xff, 0xff, 0x20, 0xe6, 0xde, 0x28, 0xc8, 0x00, 0x00, 0x00,
}
