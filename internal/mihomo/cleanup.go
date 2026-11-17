package mihomo

import (
	"os"
	"path/filepath"
)

// legacyGeodataNames 是 0.5.x 及更早版本为 GEOIP/GEOSITE 规则预置的地理数据。
// 现在配置里既没有 GEOSITE 也没有 GEOIP 规则，这些文件纯粹占磁盘：
// geosite 4.2MB + geoip 15.7MB + mmdb 7.3MB，安装根还可能留一份 geosite 副本。
var legacyGeodataNames = []string{"GeoSite.dat", "geoip.dat", "country.mmdb"}

// RemoveLegacyGeodata 删除安装根、state/ 与 runtime/ 下遗留的地理数据文件，
// 返回实际删掉的路径。删不掉（被占用）不报错，下次启动再试。
func RemoveLegacyGeodata(root string) []string {
	if root == "" {
		root = "."
	}
	var removed []string
	for _, dir := range []string{root, filepath.Join(root, "state"), filepath.Join(root, "runtime")} {
		for _, name := range legacyGeodataNames {
			path := filepath.Join(dir, name)
			if !isRegularFile(path) {
				continue
			}
			if err := os.Remove(path); err == nil {
				removed = append(removed, path)
			}
		}
	}
	return removed
}
