//go:build windows

package runlog

import "golang.org/x/sys/windows"

// diskFree 目录所在卷的剩余字节数(按大小滚动与磁盘不足告警用;
// 失败返回 ok=false,调用方静默跳过——日志设施自身不报障)。
func diskFree(dir string) (free int64, ok bool) {
	var freeBytes, total, avail uint64
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	if err := windows.GetDiskFreeSpaceEx(p, &freeBytes, &total, &avail); err != nil {
		return 0, false
	}
	return int64(freeBytes), true
}
