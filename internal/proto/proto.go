// Package proto 定义 ipsend 的 HTTP 协议数据结构（见 SPEC.md §3）。
package proto

import (
	"encoding/hex"

	"github.com/zeebo/xxh3"
)

const (
	AppName     = "ipsend"
	Version     = "1.0.0"
	ProtoVer    = 1
	DefaultPort = 52100

	MaxOfferBytes = 16 << 20
	MaxOfferFiles = 100000
)

type Ping struct {
	App     string `json:"app"`
	Version string `json:"version"`
	Proto   int    `json:"proto"`
	Name    string `json:"name"`
	PIN     bool   `json:"pin,omitempty"` // 对方要求 PIN
}

type FileEntry struct {
	FID   int    `json:"fid"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

type Offer struct {
	TransferID string      `json:"transfer_id"`
	SenderName string      `json:"sender_name"`
	Files      []FileEntry `json:"files"`
	Dirs       []string    `json:"dirs,omitempty"`
}

type FileOffset struct {
	FID    int   `json:"fid"`
	Offset int64 `json:"offset"`
	Done   bool  `json:"done,omitempty"` // 已校验并落盘
}

type OfferResp struct {
	SID    string       `json:"sid"`
	Resume bool         `json:"resume"`
	Files  []FileOffset `json:"files"`
}

type Status struct {
	Received int64 `json:"received"`
	Size     int64 `json:"size"`
	Done     bool  `json:"done"`
}

type DataResp struct {
	Received int64 `json:"received"`
}

type CommitReq struct {
	XXH3 string `json:"xxh3"`
}

type CommitResp struct {
	Name     string `json:"name"`
	Received int64  `json:"received"`
}

type FinishResp struct {
	Saved []string `json:"saved"`
}

type Error struct {
	Error    string `json:"error"`
	Message  string `json:"message,omitempty"`
	Received *int64 `json:"received,omitempty"`
}

// HashHex 把 xxh3-128 结果编码为 32 位大端 hex。
func HashHex(u xxh3.Uint128) string {
	b := u.Bytes()
	return hex.EncodeToString(b[:])
}
