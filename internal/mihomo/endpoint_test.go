package mihomo

import (
	"regexp"
	"strings"
	"testing"
)

// 测试一律使用 RFC 5737 文档地址（192.0.2.0/24）与 example 域名：
// 真实校园门户地址不应出现在开源仓库里。
const testPortalHost = "192.0.2.10"

func TestCloudProfileURLDerivation(t *testing.T) {
	// 黄金值：规则一旦改动，所有已发布客户端拼出的入口会一起失效，
	// 所以这里锁死输出，改动必须是有意识的。
	url, err := CloudProfileURL(testPortalHost)
	if err != nil {
		t.Fatalf("CloudProfileURL: %v", err)
	}
	const want = "https://68eb7f9411d22cfe.freev6.cc.cd/p"
	if url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}
}

func TestNormalizePortalHost(t *testing.T) {
	same := []string{
		testPortalHost,
		"  " + testPortalHost + "  ",
		"http://" + testPortalHost + "/",
		"https://" + testPortalHost + "/login?x=1",
		testPortalHost + ":8080",
		"HTTP://" + testPortalHost,
	}
	for _, input := range same {
		host, err := NormalizePortalHost(input)
		if err != nil {
			t.Fatalf("NormalizePortalHost(%q): %v", input, err)
		}
		if host != testPortalHost {
			t.Fatalf("NormalizePortalHost(%q) = %q", input, host)
		}
	}
	domain, err := NormalizePortalHost("https://connect.example.edu.cn/portal")
	if err != nil || domain != "connect.example.edu.cn" {
		t.Fatalf("domain normalize = %q, %v", domain, err)
	}
	for _, bad := range []string{"", "   ", "not a host", "http://"} {
		if _, err := NormalizePortalHost(bad); err == nil {
			t.Fatalf("NormalizePortalHost(%q) should fail", bad)
		}
	}
}

func TestCloudSlugShape(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := make(map[string]bool)
	for _, host := range []string{testPortalHost, "192.0.2.11", "connect.example.edu.cn"} {
		slug, err := CloudSlug(host)
		if err != nil {
			t.Fatalf("CloudSlug(%q): %v", host, err)
		}
		if !pattern.MatchString(slug) {
			t.Fatalf("slug %q is not a valid DNS label", slug)
		}
		if seen[slug] {
			t.Fatalf("slug collision on %q", host)
		}
		seen[slug] = true
		// 确定性：同一输入必须永远得到同一入口
		again, _ := CloudSlug(host)
		if again != slug {
			t.Fatalf("not deterministic: %q vs %q", slug, again)
		}
	}
}

// 派生只用于"别把域名硬编码进仓库"，不是门禁：URL 里必须能看出域名后缀，
// 但不能是维护者域名本身（避免被爬虫直接抓）。
func TestCloudProfileURLHidesBareDomain(t *testing.T) {
	url, err := CloudProfileURL(testPortalHost)
	if err != nil {
		t.Fatalf("CloudProfileURL: %v", err)
	}
	if !strings.HasPrefix(url, "https://") || !strings.HasSuffix(url, CloudProfileDomainSuffix+cloudProfilePath) {
		t.Fatalf("unexpected url %q", url)
	}
	if strings.Contains(url, "://freev6.cc.cd") {
		t.Fatalf("url must not point at the apex domain: %q", url)
	}
}
