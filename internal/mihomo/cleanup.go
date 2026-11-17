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

// RemoveStaleRuleSetFiles 删除 state/ruleset/ 下不再被引用的规则集缓存。
//
// 规则集只在文件过期时才重新下载，列表换过之后旧文件不会自己消失；留着既
// 占空间，也会在"序号复用"的写法下被误当成新列表加载。现在 provider 用
// 语义名做文件名，清理就是"不认识的都删"。
func RemoveStaleRuleSetFiles(root string) []string {
	if root == "" {
		root = "."
	}
	dir := filepath.Join(root, "state", "ruleset")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	keep := make(map[string]bool, len(ruleSets))
	for _, name := range RuleSetFileNames() {
		keep[name] = true
	}
	var removed []string
	for _, entry := range entries {
		if entry.IsDir() || keep[entry.Name()] {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err == nil {
			removed = append(removed, path)
		}
	}
	return removed
}
