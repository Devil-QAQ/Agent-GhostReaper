// Package buildinfo 承载构建期身份信息(P3-13b ③ 运行日志的日志头与
// ④ 诊断包都要带版本号)。Version 由 Makefile 经 -ldflags 注入;裸
// go build / go test 不注入时为 "dev"(开发构建,如实呈现)。
package buildinfo

// Version 版本号(Makefile 注入;缺省 dev)。
var Version = "dev"
