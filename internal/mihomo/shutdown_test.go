package mihomo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 停止前关 TUN 走的是内核的 PATCH /configs，并且必须是"只带 tun 的局部更新"：
// 整份配置提交会把运行中的模式/规则一起覆盖掉。
func TestDisableTunPatchesOnlyTun(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotMethod, gotPath = request.Method, request.URL.Path
		gotAuth = request.Header.Get("Authorization")
		data, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := DisableTun(context.Background(), server.URL, "s3cret"); err != nil {
		t.Fatalf("DisableTun: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/configs" {
		t.Errorf("path = %s", gotPath)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if len(gotBody) != 1 {
		t.Fatalf("body should carry only tun, got %v", gotBody)
	}
	tun, ok := gotBody["tun"].(map[string]any)
	if !ok {
		t.Fatalf("tun field missing: %v", gotBody)
	}
	if enabled, ok := tun["enable"].(bool); !ok || enabled {
		t.Errorf(`tun.enable should be false, got %v`, tun["enable"])
	}
}

// 控制器拒绝（密钥不符、旧内核不认识字段）时必须报错，让调用方走硬杀兜底，
// 而不是以为 TUN 已经关掉。
func TestDisableTunReportsRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	err := DisableTun(context.Background(), server.URL, "")
	if err == nil {
		t.Fatal("401 被当成成功")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误信息应带状态码: %v", err)
	}
}

// Stop 只发 Kill 就返回，进程可能还没退出：等它等的是 9090 端口释放，不等就会
// 让紧接着的启动报"端口被占用"。
func TestWaitExitPollsUntilProcessIsGone(t *testing.T) {
	calls := 0
	running := func(int) bool {
		calls++
		return calls < 3 // 前两次还在，第三次消失
	}
	if !waitExit(4242, running, time.Second, time.Millisecond) {
		t.Fatal("进程退出后仍报告未退出")
	}
	if calls < 3 {
		t.Errorf("只轮询了 %d 次", calls)
	}
}

// 超时不算错误：进程终究会死，不能因为等不到就把停止判失败。
func TestWaitExitGivesUpWithoutHanging(t *testing.T) {
	started := time.Now()
	if waitExit(4242, func(int) bool { return true }, 30*time.Millisecond, time.Millisecond) {
		t.Fatal("进程没退出却报告已退出")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("等了 %v，应在上限内放弃", elapsed)
	}
}
