package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DisableTun 让正在运行的内核自己把 TUN 设备关掉。
//
// 停止免流原本直接 TerminateProcess：内核没机会关掉 wintun 会话，适配器和
// auto-route 写进路由表的 ::/0、0.0.0.0/1 只能等 Windows 在进程死后异步回收。
// 那几秒里所有出站流量仍然被吸进一个没人接收的 TUN，用户看到的就是「点了
// 停止，网还要过十几秒才恢复」，紧接着点启动也会因为路由没收敛而检测失败。
//
// 先 PATCH /configs 把 tun.enable 关掉，wintun 会话由内核同步关闭、适配器
// 随即从系统消失、默认路由回到物理网卡；之后再把进程杀掉，收尾就是百毫秒级。
// 失败（旧内核不认识这个字段、控制器没起来）不构成错误路径：调用方照旧硬杀。
func DisableTun(ctx context.Context, address, secret string) error {
	if strings.TrimSpace(address) == "" {
		return errors.New("mihomo controller address is empty")
	}
	baseURL := address
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, baseURL+"/configs",
		strings.NewReader(`{"tun":{"enable":false}}`))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// 内核对这个 PATCH 回 204；200 也认。其余状态（401 密钥不符、400 不认识
	// 的字段）都说明 TUN 还在，交给调用方的兜底路径。
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("mihomo PATCH /configs: HTTP %s", response.Status)
	}
	return nil
}

// StopAndWait 杀掉内核，并等它真的退出。
//
// Stop 用的是 Kill（TerminateProcess），返回时进程往往还在收尾：控制器 9090
// 端口没释放，紧接着的启动会在 CheckListenPorts 上撞出「端口被占用」，前端把
// 它当成"别的代理软件占着端口"报错——刚停完立刻启动就踩这个。等一小会儿
// （实际通常 20–100ms）可以把这条确定性失败消掉。
// 返回等待耗时与是否观察到退出；超时不算失败，进程终究会死。
func StopAndWait(pidPath string, timeout time.Duration) (time.Duration, bool, error) {
	pid, running, err := Status(pidPath)
	if err != nil {
		return 0, false, err
	}
	if err := Stop(pidPath); err != nil {
		return 0, false, err
	}
	if !running || pid <= 0 {
		return 0, true, nil
	}
	started := time.Now()
	exited := waitExit(pid, IsRunning, timeout, 10*time.Millisecond)
	return time.Since(started), exited, nil
}

// waitExit 轮询进程是否真的退出。running 可注入，测试不必起真实进程。
func waitExit(pid int, running func(int) bool, timeout, poll time.Duration) bool {
	if poll <= 0 {
		poll = 10 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		if !running(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(poll)
	}
}
