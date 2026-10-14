//go:build windows

package procattr

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	tcpTableOwnerPIDAll = 5 // TCP_TABLE_OWNER_PID_ALL
	afInet              = 2
	afInet6             = 23
	mibTCPStateEstab    = 5
)

type windowsPlatform struct{}

func platform() Platform { return windowsPlatform{} }

// tcpTable 调 GetExtendedTcpTable，返回原始缓冲区（先问大小，再取）。
func tcpTable(family uint32) ([]byte, error) {
	var size uint32
	for i := 0; i < 4; i++ {
		var p uintptr
		buf := make([]byte, size)
		if size > 0 {
			p = uintptr(unsafe.Pointer(&buf[0]))
		}
		r, _, _ := procGetExtendedTcpTable.Call(p, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDAll, 0)
		switch windows.Errno(r) {
		case 0:
			return buf[:size], nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			size += 4096
			continue
		default:
			return nil, fmt.Errorf("GetExtendedTcpTable 失败：%w", windows.Errno(r))
		}
	}
	return nil, fmt.Errorf("GetExtendedTcpTable：缓冲区一直不够")
}

func port(dw uint32) int {
	// dwLocalPort 低 16 位是网络字节序
	return int(uint16(dw>>8&0xff) | uint16(dw&0xff)<<8)
}

func (windowsPlatform) PortPIDs() (map[int]int, error) {
	m := map[int]int{}
	// MIB_TCPROW_OWNER_PID：state, localAddr, localPort, remoteAddr, remotePort, owningPid（6 个 DWORD）
	b, err := tcpTable(afInet)
	if err != nil {
		return nil, err
	}
	if len(b) >= 4 {
		n := *(*uint32)(unsafe.Pointer(&b[0]))
		const row = 24
		for i := uint32(0); i < n && int(4+(i+1)*row) <= len(b); i++ {
			r := b[4+i*row:]
			state := *(*uint32)(unsafe.Pointer(&r[0]))
			lport := *(*uint32)(unsafe.Pointer(&r[8]))
			pid := *(*uint32)(unsafe.Pointer(&r[20]))
			if state == mibTCPStateEstab {
				m[port(lport)] = int(pid)
			}
		}
	}
	// MIB_TCP6ROW_OWNER_PID：localAddr[16], localScope, localPort, remoteAddr[16], remoteScope, remotePort, state, pid
	if b6, err := tcpTable(afInet6); err == nil && len(b6) >= 4 {
		n := *(*uint32)(unsafe.Pointer(&b6[0]))
		const row = 56
		for i := uint32(0); i < n && int(4+(i+1)*row) <= len(b6); i++ {
			r := b6[4+i*row:]
			lport := *(*uint32)(unsafe.Pointer(&r[20]))
			state := *(*uint32)(unsafe.Pointer(&r[48]))
			pid := *(*uint32)(unsafe.Pointer(&r[52]))
			if state == mibTCPStateEstab {
				if _, dup := m[port(lport)]; !dup {
					m[port(lport)] = int(pid)
				}
			}
		}
	}
	return m, nil
}

// AppNames 取进程映像路径的文件名，去掉 .exe。
func (windowsPlatform) AppNames(pids []int) map[int]string {
	m := map[int]string{}
	for _, pid := range pids {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		err = windows.QueryFullProcessImageName(h, 0, &buf[0], &n)
		windows.CloseHandle(h)
		if err != nil {
			continue
		}
		name := filepath.Base(windows.UTF16ToString(buf[:n]))
		if strings.HasSuffix(strings.ToLower(name), ".exe") {
			name = name[:len(name)-4]
		}
		m[pid] = name
	}
	return m
}
