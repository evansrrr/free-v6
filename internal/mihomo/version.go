package mihomo

import (
	"fmt"
	"strconv"
	"strings"
)

// CoreVersion 是内核版本标识的解析结果。
//
// 两种命名并存：
//   - f6-core：core-v0.1.0-rc.2（tag 原样）
//   - 上游 mihomo 遗留内核：v1.19.31 / 1.10.0（旧版 FreeV6 下载的 58MB 那个）
//
// 遗留内核永远判定为“更旧”：它们不含本项目的定制，任何 f6-core 版本都应
// 覆盖它们。这条规则是迁移路径的全部依据——不需要额外的状态位。
type CoreVersion struct {
	raw    string
	parts  [3]int
	pre    string // "-rc.2" 里的 "rc.2"，空表示正式版
	legacy bool
	valid  bool
}

// ParseCoreVersion 容忍前导空格、大小写与前缀差异；解析失败时 valid=false。
func ParseCoreVersion(raw string) CoreVersion {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return CoreVersion{}
	}
	version := CoreVersion{raw: strings.TrimSpace(raw), valid: true}
	switch {
	case strings.HasPrefix(value, "core-v"):
		value = strings.TrimPrefix(value, "core-v")
	case strings.HasPrefix(value, "core-"):
		value = strings.TrimPrefix(value, "core-")
		value = strings.TrimPrefix(value, "v")
	case strings.HasPrefix(value, "v"):
		value = strings.TrimPrefix(value, "v")
		version.legacy = true
	default:
		version.legacy = true
	}
	if cut := strings.Index(value, "-"); cut >= 0 {
		version.pre = value[cut+1:]
		value = value[:cut]
	} else if cut := strings.Index(value, "+"); cut >= 0 {
		value = value[:cut]
	}
	fields := strings.Split(value, ".")
	if len(fields) == 0 || len(fields) > 3 {
		return CoreVersion{raw: raw}
	}
	for index, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil || number < 0 {
			return CoreVersion{raw: raw}
		}
		version.parts[index] = number
	}
	return version
}

// Valid 报告解析是否成功。空版本（未安装内核）返回 false。
func (v CoreVersion) Valid() bool { return v.valid }

// Legacy 报告是否为上游 mihomo 遗留内核。
func (v CoreVersion) Legacy() bool { return v.legacy }

// Display 返回给用户看的版本号：f6-core 统一成 v0.1.0-rc.2 的形状，
// 遗留内核原样返回它自报的版本（v1.19.31 / 1.10.0）。
func (v CoreVersion) Display() string {
	if !v.valid {
		return ""
	}
	if v.legacy {
		return v.raw
	}
	display := fmt.Sprintf("v%d.%d.%d", v.parts[0], v.parts[1], v.parts[2])
	if v.pre != "" {
		display += "-" + v.pre
	}
	return display
}

// Compare 返回 -1 / 0 / 1。遗留内核恒小于任何 f6-core 版本，
// 两个遗留内核之间按数字比较（1.10.0 < 1.19.31）。
func (v CoreVersion) Compare(other CoreVersion) int {
	switch {
	case !v.valid && !other.valid:
		return 0
	case !v.valid:
		return -1
	case !other.valid:
		return 1
	case v.legacy != other.legacy:
		if v.legacy {
			return -1
		}
		return 1
	}
	for index := range v.parts {
		if v.parts[index] != other.parts[index] {
			if v.parts[index] < other.parts[index] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.pre == other.pre:
		return 0
	case v.pre == "":
		return 1 // 正式版高于任何预发布
	case other.pre == "":
		return -1
	}
	return comparePrerelease(v.pre, other.pre)
}

// comparePrerelease 按 semver 规则比较预发布段：逐段比，数字段按数值、
// 其余按字典序，数字段低于字母段（rc.2 < rc.10 < rc.beta）。
func comparePrerelease(a, b string) int {
	left := strings.Split(a, ".")
	right := strings.Split(b, ".")
	for index := 0; index < len(left) && index < len(right); index++ {
		if left[index] == right[index] {
			continue
		}
		leftNumber, leftErr := strconv.Atoi(left[index])
		rightNumber, rightErr := strconv.Atoi(right[index])
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			return 1
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		case left[index] < right[index]:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	}
	return 0
}

// ProbeVersion 从内核二进制的 `-v` 输出里取版本。输出形如：
// "Mihomo Meta core-v0.1.0-rc.2 windows amd64 with go1.26.3 ..."
func ProbeVersion(versionOutput string) CoreVersion {
	for _, line := range strings.Split(versionOutput, "\n") {
		fields := strings.Fields(line)
		for index, field := range fields {
			if strings.EqualFold(field, "Meta") && index+1 < len(fields) {
				return ParseCoreVersion(fields[index+1])
			}
		}
	}
	return CoreVersion{}
}
