//go:build windows

package network

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	ole "github.com/go-ole/go-ole"
)

// hnshareProgID 是 Windows 家庭网络共享配置管理器（HNetCfg）的 ProgID，
// 其 CLSID 从注册表解析，避免硬编码 GUID。
const hnshareProgID = "HNetCfg.HNetShare"

// withHnshare 在当前 OS 线程上初始化 COM、创建 INetSharingManager 并执行 fn。
// 全程 runtime.LockOSThread 固定线程，defer 保证 COM 卸载与对象释放，避免
// goroutine 被调度到其它 OS 线程后 COM 初始化失效、或引用计数泄漏。
//
// HNetCfg.HNetShare 的 ThreadingModel 是 "Both"，用 MTA 初始化即可安全地从
// 任意线程调用（无跨单元 marshaling）。
func withHnshare(fn func(mgr *ole.IDispatch) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		return fmt.Errorf("CoInitializeEx: %w", err)
	}
	defer ole.CoUninitialize()

	clsid, err := ole.CLSIDFromProgID(hnshareProgID)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", hnshareProgID, err)
	}
	unk, err := ole.CreateInstance(clsid, ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("create %s: %w", hnshareProgID, err)
	}
	mgr := (*ole.IDispatch)(unsafe.Pointer(unk))
	defer mgr.Release()
	return fn(mgr)
}

// hnshareEnumerate 枚举所有 HNetShare 连接，对每个连接读属性后回调 visit。
// visit 里可借 conn 调用 hnshareSetConnSharing 修改该连接；conn 引用由外层
// VARIANT 持有，仅在 visit 执行期间有效。
func hnshareEnumerate(mgr *ole.IDispatch, visit func(conn *ole.IDispatch, c hnshareConn) error) error {
	collVar, err := mgr.GetProperty("EnumEveryConnection")
	if err != nil {
		return fmt.Errorf("EnumEveryConnection: %w", err)
	}
	defer collVar.Clear()
	coll := collVar.ToIDispatch()
	if coll == nil {
		return fmt.Errorf("EnumEveryConnection returned no collection")
	}

	enumVar, err := coll.GetProperty("_NewEnum")
	if err != nil {
		return fmt.Errorf("_NewEnum: %w", err)
	}
	defer enumVar.Clear()
	enum, err := enumVar.ToIUnknown().IEnumVARIANT(ole.IID_IEnumVariant)
	if err != nil {
		return fmt.Errorf("IEnumVARIANT: %w", err)
	}
	defer enum.Release()

	for {
		item, length, err := enum.Next(1)
		if err != nil {
			return fmt.Errorf("enumerate connections: %w", err)
		}
		if length == 0 {
			break
		}
		if conn := item.ToIDispatch(); conn != nil {
			c, rerr := hnshareReadConn(mgr, conn)
			if rerr == nil {
				rerr = visit(conn, c)
			}
			item.Clear()
			if rerr != nil {
				return rerr
			}
		} else {
			item.Clear()
		}
	}
	return nil
}

// hnshareGetInterface 获取「带对象参数的属性」（NetConnectionProps /
// INetSharingConfigurationForINetConnection）。netcon.idl 里它们是 propget，
// 但 PowerShell 传统上用 METHOD 语义调用；为兼容两类实现，PROPERTYGET 失败
// 时退回 METHOD 再试一次。
func hnshareGetInterface(mgr *ole.IDispatch, name string, conn *ole.IDispatch) (*ole.VARIANT, error) {
	v, err := mgr.GetProperty(name, conn)
	if err != nil {
		return mgr.CallMethod(name, conn)
	}
	return v, nil
}

// hnshareReadConn 读一个连接的名称/GUID/设备名与共享状态。
func hnshareReadConn(mgr, conn *ole.IDispatch) (hnshareConn, error) {
	var c hnshareConn

	propsVar, err := hnshareGetInterface(mgr, "NetConnectionProps", conn)
	if err != nil {
		return c, fmt.Errorf("NetConnectionProps: %w", err)
	}
	props := propsVar.ToIDispatch()
	if props == nil {
		propsVar.Clear()
		return c, fmt.Errorf("NetConnectionProps returned no props")
	}
	c.name = hnsharePropString(props, "Name")
	c.guid = hnsharePropString(props, "Guid")
	c.device = hnsharePropString(props, "DeviceName")
	propsVar.Clear()

	cfgVar, err := hnshareGetInterface(mgr, "INetSharingConfigurationForINetConnection", conn)
	if err != nil {
		return c, fmt.Errorf("INetSharingConfigurationForINetConnection: %w", err)
	}
	defer cfgVar.Clear()
	cfg := cfgVar.ToIDispatch()
	if cfg == nil {
		return c, fmt.Errorf("sharing configuration missing")
	}
	c.enabled = hnsharePropBool(cfg, "SharingEnabled")
	if t, ok := hnsharePropInt(cfg, "SharingType"); ok {
		if t == 1 {
			c.kind = "private"
		} else {
			c.kind = "public"
		}
	}
	return c, nil
}

// hnsharePropString 读 dispatch 的字符串属性，失败返回空串。
func hnsharePropString(disp *ole.IDispatch, name string) string {
	v, err := disp.GetProperty(name)
	if err != nil {
		return ""
	}
	defer v.Clear()
	return v.ToString()
}

// hnsharePropBool 读 dispatch 的布尔属性。
func hnsharePropBool(disp *ole.IDispatch, name string) bool {
	v, err := disp.GetProperty(name)
	if err != nil {
		return false
	}
	defer v.Clear()
	b, _ := v.Value().(bool)
	return b
}

// hnsharePropInt 读 dispatch 的整数属性。
func hnsharePropInt(disp *ole.IDispatch, name string) (int32, bool) {
	v, err := disp.GetProperty(name)
	if err != nil {
		return 0, false
	}
	defer v.Clear()
	t, ok := v.Value().(int32)
	return t, ok
}

// hnshareSetConnSharing 把连接设为指定共享角色：0=public、1=private、
// -1=禁用共享。
func hnshareSetConnSharing(mgr, conn *ole.IDispatch, sharingType int) error {
	cfgVar, err := hnshareGetInterface(mgr, "INetSharingConfigurationForINetConnection", conn)
	if err != nil {
		return fmt.Errorf("INetSharingConfigurationForINetConnection: %w", err)
	}
	defer cfgVar.Clear()
	cfg := cfgVar.ToIDispatch()
	if cfg == nil {
		return fmt.Errorf("sharing configuration missing")
	}
	if sharingType < 0 {
		if _, err := cfg.CallMethod("DisableSharing"); err != nil {
			return fmt.Errorf("DisableSharing: %w", err)
		}
		return nil
	}
	if _, err := cfg.CallMethod("EnableSharing", int(sharingType)); err != nil {
		return fmt.Errorf("EnableSharing(%d): %w", sharingType, err)
	}
	return nil
}

// hnshareSetByName 枚举连接，把名字匹配的连接设为指定角色；找不到返回错误。
// 已处于目标状态则跳过（幂等）；已共享但角色不同则先禁用再启用（与原脚本
// 一致，避免 EnableSharing 对已共享连接的行为不稳定）。
func hnshareSetByName(mgr *ole.IDispatch, name string, sharingType int) error {
	found := false
	wantKind := "public"
	if sharingType == 1 {
		wantKind = "private"
	}
	err := hnshareEnumerate(mgr, func(conn *ole.IDispatch, c hnshareConn) error {
		if c.name != name {
			return nil
		}
		found = true
		if c.enabled && c.kind == wantKind {
			return nil
		}
		if c.enabled {
			if err := hnshareSetConnSharing(mgr, conn, -1); err != nil {
				return err
			}
		}
		return hnshareSetConnSharing(mgr, conn, sharingType)
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("connection %q not found", name)
	}
	return nil
}

// CaptureSharing 枚举所有连接的共享状态作为基线。先走进程内 COM 直调
// （快、无子进程）；COM 失败或返回空（健康机器必有多个连接，空即说明 COM
// 路径在这台机器上坏了）时退回 PowerShell 脚本兜底，保证热点共享的基线抓取
// 不再因为 COM 环境差异而失效。
func CaptureSharing(ctx context.Context) ([]SharingSnapshot, error) {
	result, comErr := captureSharingCOM()
	if comErr == nil && len(result) > 0 {
		return result, nil
	}
	ps, psErr := captureSharingPowerShell(ctx)
	if psErr != nil {
		if comErr != nil {
			return nil, fmt.Errorf("capture ICS sharing state (COM: %v; PowerShell: %v)", comErr, psErr)
		}
		return nil, fmt.Errorf("capture ICS sharing state (COM returned empty; PowerShell: %v)", psErr)
	}
	return ps, nil
}

// captureSharingCOM 进程内枚举共享基线（快路径）。
func captureSharingCOM() ([]SharingSnapshot, error) {
	var result []SharingSnapshot
	err := withHnshare(func(mgr *ole.IDispatch) error {
		return hnshareEnumerate(mgr, func(_ *ole.IDispatch, c hnshareConn) error {
			if c.name == "" && c.guid == "" {
				return nil
			}
			if c.kind != "public" && c.kind != "private" {
				return nil
			}
			result = append(result, SharingSnapshot{Name: c.name, Guid: c.guid, Device: c.device, Enabled: c.enabled, Kind: c.kind})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// captureSharingPowerShell 用脚本枚举共享基线（兜底路径）。
func captureSharingPowerShell(ctx context.Context) ([]SharingSnapshot, error) {
	output, err := runPowerShell(ctx, PowerShellSharingCaptureCommand)
	if err != nil {
		return nil, err
	}
	return ParsePowerShellSharing(output)
}

// SwitchICS 经典路径：FreeV6TUN=PUBLIC、热点私有侧=PRIVATE。
// 先切 TUN 再切私有侧：TUN 不存在时立即失败，不把热点私有侧留在半切换态。
// COM 直调失败时退回 PowerShell 脚本重试（脚本幂等，已处于目标角色则跳过）。
func SwitchICS(ctx context.Context, privateAlias string) error {
	comErr := withHnshare(func(mgr *ole.IDispatch) error {
		if err := hnshareSetByName(mgr, TunDeviceName, 0); err != nil {
			return fmt.Errorf("switch ICS topology (TUN): %w", err)
		}
		if err := hnshareSetByName(mgr, privateAlias, 1); err != nil {
			return fmt.Errorf("switch ICS topology (hotspot): %w", err)
		}
		return nil
	})
	if comErr == nil {
		return nil
	}
	if _, err := runPowerShell(ctx, BuildICSSwitchScript(privateAlias)); err != nil {
		return fmt.Errorf("switch ICS topology (COM: %v; PowerShell: %w)", comErr, err)
	}
	return nil
}

// RestoreICSSharing 把 ICS 共享状态还原到基线；单连接失败汇总在错误里。
// COM 直调失败时退回 PowerShell 脚本兜底。
func RestoreICSSharing(ctx context.Context, snapshot Snapshot) error {
	comErr := restoreICSSharingCOM(snapshot)
	if comErr == nil {
		return nil
	}
	if psErr := restoreICSSharingPowerShell(ctx, snapshot); psErr != nil {
		return fmt.Errorf("restore ICS sharing (COM: %v; PowerShell: %v)", comErr, psErr)
	}
	return nil
}

// restoreICSSharingCOM 进程内按基线还原共享角色（快路径）。
func restoreICSSharingCOM(snapshot Snapshot) error {
	var issues []string
	err := withHnshare(func(mgr *ole.IDispatch) error {
		return hnshareEnumerate(mgr, func(conn *ole.IDispatch, c hnshareConn) error {
			if c.guid == "" {
				return nil
			}
			for _, action := range restoreActions(c, snapshot.Sharing, TunDeviceName) {
				shareType := -1
				switch action {
				case restoreEnablePublic:
					shareType = 0
				case restoreEnablePrivate:
					shareType = 1
				case restoreDisable:
					shareType = -1
				default:
					continue
				}
				if err := hnshareSetConnSharing(mgr, conn, shareType); err != nil {
					issues = append(issues, c.name+": "+err.Error())
				}
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		return fmt.Errorf("%s", strings.Join(issues, "; "))
	}
	return nil
}

// restoreICSSharingPowerShell 用脚本按基线还原共享角色（兜底路径）。
func restoreICSSharingPowerShell(ctx context.Context, snapshot Snapshot) error {
	output, err := runPowerShell(ctx, BuildICSRestoreScript(snapshot.Sharing, TunDeviceName))
	if err != nil {
		return err
	}
	var result struct {
		Errors []string `json:"errors"`
	}
	if err := jsonUnmarshalFlexible(output, &result); err != nil {
		return err
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("%s", strings.Join(result.Errors, "; "))
	}
	return nil
}
