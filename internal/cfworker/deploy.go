package cfworker

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// neutralWords are business-sounding names that avoid proxy/risk keywords.
var neutralWords = []string{
	"aurora", "breeze", "cascade", "dune", "ember", "falcon", "glacier",
	"harbor", "iris", "juniper", "keystone", "lumen", "meadow", "nimbus",
	"onyx", "pebble", "quartz", "ridge", "solstice", "tundra", "umbra",
	"vale", "willow", "xenon", "yarrow", "zenith", "alder", "birch",
}

// AccountCredentials is what the fleet knows about a CF account.
type AccountCredentials struct {
	Email     string
	AccountID string
	APIToken  string
}

// DeployResult describes a freshly deployed worker.
type DeployResult struct {
	Name         string
	CanonicalURL string
}

// NeutralName generates an unused neutral worker name: <word><2-digit>.
func NeutralName(existing map[string]bool) string {
	for attempt := 0; attempt < 200; attempt++ {
		word := neutralWords[randomInt(len(neutralWords))]
		name := fmt.Sprintf("%s%d", word, 10+randomInt(90))
		if !existing[name] {
			return name
		}
	}
	return fmt.Sprintf("node%d", randomInt(1_000_000))
}

func randomInt(max int) int {
	if max <= 0 {
		return 0
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return int(value.Int64())
}

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomToken(length int) string {
	var out strings.Builder
	for i := 0; i < length; i++ {
		out.WriteByte(tokenAlphabet[randomInt(len(tokenAlphabet))])
	}
	return out.String()
}

// Deploy deploys one BPB worker and returns its subscription URL.
func Deploy(
	ctx context.Context,
	client *Client,
	account AccountCredentials,
	dataDir string,
) (DeployResult, error) {
	subdomain, err := client.EnsureSubdomain(ctx, account.AccountID, "")
	if err != nil {
		return DeployResult{}, fmt.Errorf("确保子域失败: %w", err)
	}
	workers, err := client.ListWorkers(ctx, account.AccountID)
	if err != nil {
		return DeployResult{}, fmt.Errorf("列出 worker 失败: %w", err)
	}
	existing := make(map[string]bool, len(workers))
	for _, worker := range workers {
		if worker.ID != "" {
			existing[worker.ID] = true
		}
	}
	name := NeutralName(existing)

	script, err := FetchWorkerScript(ctx, dataDir)
	if err != nil {
		return DeployResult{}, err
	}
	vlUUID := randomUUID()
	securePath := randomToken(24)
	trPass := randomToken(24)
	mainDomain := name + "." + subdomain + ".workers.dev"
	settings := map[string]string{
		"accID":       account.AccountID,
		"accEmail":    account.Email,
		"apiToken":    account.APIToken,
		"vlUUID":      vlUUID,
		"trPass":      trPass,
		"securePath":  securePath,
		"proxyIpMode": "0",
		"proxyIPs":    "",
		"prefixes":    "",
		"fallback":    "",
		"dohUrl":      "https://cloudflare-dns.com/dns-query",
		"mainDomain":  mainDomain,
	}
	injected := InjectSettings(script, settings)

	// KV namespace (shared per account) + panel password: reuse by title
	kvTitle := "bpb-kv-" + truncateAccount(account.AccountID)
	kvNamespaceID, err := client.FindKVNamespace(ctx, account.AccountID, kvTitle)
	if err != nil {
		return DeployResult{}, fmt.Errorf("查询 KV 失败: %w", err)
	}
	if kvNamespaceID == "" {
		kvNamespaceID, err = client.CreateKVNamespace(ctx, account.AccountID, kvTitle)
		if err != nil {
			return DeployResult{}, fmt.Errorf("创建 KV 失败: %w", err)
		}
	}
	if err := client.UploadWorker(ctx, account.AccountID, name, injected, kvNamespaceID, nil); err != nil {
		return DeployResult{}, fmt.Errorf("上传 worker 失败: %w", err)
	}
	if err := client.EnableScriptSubdomain(ctx, account.AccountID, name); err != nil {
		return DeployResult{}, fmt.Errorf("启用子域失败: %w", err)
	}
	_ = client.WriteKVValue(ctx, account.AccountID, kvNamespaceID, "pwd", randomToken(20))

	workerURL := "https://" + mainDomain
	return DeployResult{
		Name:         name,
		CanonicalURL: canonicalSubscriptionURL(workerURL, securePath),
	}, nil
}

func truncateAccount(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
