package residential

import "slices"

// Rotation modes describe how a vendor hands out new exit IPs.
const (
	// RotationSessionTemplate encodes a sticky session id in the username. A
	// new session id yields a new exit IP; the same id keeps one.
	RotationSessionTemplate = "session-template"
	// RotationPerRequest means the gateway rotates on its own per request or
	// per connection and offers no session pinning.
	RotationPerRequest = "per-request"
	// RotationAPIList means exit endpoints are fetched from a vendor HTTP API
	// rather than derived from a username.
	RotationAPIList = "api-list"
)

// Preset is a vendor-specific starting point for a provider configuration.
//
// Presets are an explicit registry rather than a plugin system: adding a vendor
// means appending one literal here. Verified reports whether the field values
// were confirmed against the vendor's own documentation — an unverified preset
// still works, but the operator must confirm the rendered username with the
// provider test probe before trusting it.
type Preset struct {
	Vendor            string `json:"vendor"`
	Label             string `json:"label"`
	Protocol          string `json:"protocol"`
	GatewayHost       string `json:"gateway_host"`
	GatewayPort       int    `json:"gateway_port"`
	UsernameTemplate  string `json:"username_template"`
	RotationMode      string `json:"rotation_mode"`
	SessionTTLSeconds int    `json:"session_ttl_seconds"`
	PoolSize          int    `json:"pool_size"`
	// Verified is false when the gateway syntax could not be confirmed against
	// vendor documentation. The UI surfaces this so an operator knows to verify
	// the template with a test connection before relying on it.
	Verified bool   `json:"verified"`
	DocURL   string `json:"doc_url,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

// presets is the registered vendor list, ordered for display.
var presets = []Preset{
	{
		Vendor:            "bestproxy",
		Label:             "BestProxy",
		Protocol:          "http",
		GatewayHost:       "proxy.bestproxy.com",
		GatewayPort:       2312,
		UsernameTemplate:  "{user}_area-{region}_life-{ttl}_session-{session}",
		RotationMode:      RotationSessionTemplate,
		SessionTTLSeconds: 60,
		PoolSize:          8,
		Verified:          true,
		DocURL:            "https://bestproxy.com",
		Notes: "BestProxy 动态住宅粘性账号语法（已在 hx-auto-outlook 中验证）：" +
			"`账号_area-国家_life-分钟_session-会话ID`。网关为 proxy.bestproxy.com:2312；" +
			"国家代码请填大写（如 US），系统会原样保留；life 单位为分钟，" +
			"session_ttl_seconds 建议填 30-90；客户端建立逻辑会话时系统才生成供应商会话 ID，" +
			"不同客户端会话获得独立出口 IP。保存后用「测试连接」确认出口 IP。",
	},
	{
		Vendor:            "generic-sticky",
		Label:             "通用 · 粘滞会话网关",
		Protocol:          "http",
		GatewayHost:       "",
		GatewayPort:       0,
		UsernameTemplate:  "{user}-session-{session}",
		RotationMode:      RotationSessionTemplate,
		SessionTTLSeconds: 600,
		PoolSize:          8,
		Verified:          true,
		Notes:             "适用于把 sticky session id 编码在用户名里的多数住宅代理供应商。",
	},
	{
		Vendor:            "generic-region-sticky",
		Label:             "通用 · 地区 + 粘滞会话",
		Protocol:          "http",
		GatewayHost:       "",
		GatewayPort:       0,
		UsernameTemplate:  "{user}-country-{country}-session-{session}",
		RotationMode:      RotationSessionTemplate,
		SessionTTLSeconds: 600,
		PoolSize:          8,
		Verified:          true,
		Notes:             "在粘滞会话之外再指定出口国家/地区。",
	},
	{
		Vendor:            "generic-rotating",
		Label:             "通用 · 每请求轮换网关",
		Protocol:          "http",
		GatewayHost:       "",
		GatewayPort:       0,
		UsernameTemplate:  "{user}",
		RotationMode:      RotationPerRequest,
		SessionTTLSeconds: 0,
		PoolSize:          1,
		Verified:          true,
		Notes:             "网关自行轮换出口 IP，不支持粘滞会话，只能用于透传模式。",
	},
	{
		Vendor:            "generic-socks5-sticky",
		Label:             "通用 · SOCKS5 粘滞会话",
		Protocol:          "socks5",
		GatewayHost:       "",
		GatewayPort:       0,
		UsernameTemplate:  "{user}-session-{session}",
		RotationMode:      RotationSessionTemplate,
		SessionTTLSeconds: 600,
		PoolSize:          8,
		Verified:          true,
		Notes:             "与粘滞会话网关相同，但上游走 SOCKS5。",
	},
	{
		Vendor:            "bestproxy-api",
		Label:             "BestProxy · API 提取",
		Protocol:          "http",
		GatewayHost:       "",
		GatewayPort:       0,
		UsernameTemplate:  "",
		RotationMode:      RotationAPIList,
		SessionTTLSeconds: 60,
		PoolSize:          8,
		Verified:          true,
		DocURL:            "https://bestproxy.com",
		Notes: "在 BestProxy 面板「API提取」生成 API 链接（包含 app_key 等参数），" +
			"完整粘贴到 api_url。客户端建立会话或换 IP 时实时请求新的 IP:port 节点。" +
			"该模式无需网关账号密码；请把本机公网出口 IP 加入 BestProxy 白名单。",
	},
}

// Presets returns a copy of the registered vendor presets.
func Presets() []Preset {
	return slices.Clone(presets)
}

// PresetByVendor looks up one preset by its vendor key.
func PresetByVendor(vendor string) (Preset, bool) {
	for _, preset := range presets {
		if preset.Vendor == vendor {
			return preset, true
		}
	}
	return Preset{}, false
}

// SupportedProtocols lists the upstream gateway protocols the data plane can
// dial. These map directly onto Mihomo outbound types, so no new protocol
// implementation is involved.
func SupportedProtocols() []string {
	return []string{"http", "https", "socks5"}
}

// SupportedRotationModes lists the accepted rotation modes.
func SupportedRotationModes() []string {
	return []string{RotationSessionTemplate, RotationPerRequest, RotationAPIList}
}
