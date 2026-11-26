package network

import (
	"errors"
	"testing"
)

// 残留 TUN 路由只在内核确认没跑、且适配器确实还在时才清。内核在跑时那些
// 路由是它的出站路径；状态读不到时也不能猜 —— 清错一次就是全网断。
// 这条判定被 helper 的启动/探测与开发用 CLI 的启动共用，所以它属于 network。
func TestShouldClearLeftoverTunRoutes(t *testing.T) {
	cases := map[string]struct {
		running   bool
		statusErr error
		present   bool
		want      bool
	}{
		"core stopped with leftover adapter": {running: false, present: true, want: true},
		"core running":                       {running: true, present: true, want: false},
		"status unknown":                     {statusErr: errors.New("status failed"), present: true, want: false},
		"no leftover adapter":                {present: false, want: false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := shouldClearLeftoverTunRoutes(c.running, c.statusErr, c.present); got != c.want {
				t.Fatalf("shouldClearLeftoverTunRoutes = %v, want %v", got, c.want)
			}
		})
	}
}
