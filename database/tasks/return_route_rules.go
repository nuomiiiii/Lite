package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logger "github.com/komari-monitor/komari/utils/log"
)

const (
	returnRouteRuleSchemaVersion      = 1
	returnRouteRuleExternalPath       = "./data/return-route-signatures.json"
	returnRouteBGPExternalPath        = "./data/return-route-bgp-prefixes.json"
	returnRouteBGPDefaultURL          = "https://raw.githubusercontent.com/nuomiiiii/komari/main/database/tasks/return_route_bgp_prefixes.json"
	returnRouteRulePollInterval       = 2 * time.Second
	returnRouteBGPRefreshInterval     = 2 * time.Hour
	returnRouteRuleMaxSize            = 2 << 20
	returnRouteBGPRuleMaxSize         = 16 << 20
	returnRouteBGPDownloadTimeout     = 30 * time.Second
)

var (
	//go:embed return_route_signatures.json
	builtinReturnRouteRuleJSON []byte

	requiredReturnRouteASNGroups = []string{
		"cmin2", "cmi", "cn2_global", "cn2_backbone", "telecom_163",
		"unicom_10099", "unicom_9929", "unicom_4837", "cmnet",
	}
	requiredReturnRouteConfidence = []string{
		"cmin2", "cmi", "cn2_gia", "cn2_gt_strong", "cn2_gt",
		"cn2_gt_prefix_only", "unicom_10099", "unicom_9929", "unicom_4837",
		"telecom_163", "telecom_163_prefix", "cmnet",
	}
	returnRouteBGPHTTPClient = &http.Client{Timeout: returnRouteBGPDownloadTimeout}
)

type ReturnRouteRuleDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	RuleVersion   string              `json:"rule_version"`
	ASNGroups     map[string][]int    `json:"asn_groups"`
	PrefixGroups  map[string][]string `json:"prefix_groups"`
	Confidence    map[string]float64  `json:"confidence"`
}

type ReturnRouteBGPRuleDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Source        string              `json:"source"`
	PrefixGroups  map[string][]string `json:"prefix_groups"`
}

type ReturnRouteRuleStatus struct {
	Source             string     `json:"source"`
	RuleVersion        string     `json:"rule_version"`
	SchemaVersion      int        `json:"schema_version"`
	LoadedAt           time.Time  `json:"loaded_at"`
	LastAttemptAt      time.Time  `json:"last_attempt_at"`
	ExternalPath       string     `json:"external_path"`
	ASNRuleCount       int        `json:"asn_rule_count"`
	ManualCIDRCount    int        `json:"manual_cidr_count"`
	BGPCIDRCount       int        `json:"bgp_cidr_count"`
	CIDRRuleCount      int        `json:"cidr_rule_count"`
	LastError          string     `json:"last_error"`
	Watching           bool       `json:"watching"`
	BGPSourceURL       string     `json:"bgp_source_url"`
	BGPGeneratedAt     *time.Time `json:"bgp_generated_at,omitempty"`
	BGPLoadedAt        *time.Time `json:"bgp_loaded_at,omitempty"`
	BGPNextRefreshAt   *time.Time `json:"bgp_next_refresh_at,omitempty"`
	BGPLastError       string     `json:"bgp_last_error"`
}

type ReturnRouteRuleView struct {
	Status ReturnRouteRuleStatus   `json:"status"`
	Rules  ReturnRouteRuleDocument `json:"rules"`
}

type returnRoutePrefixRule struct {
	group   string
	network *net.IPNet
}

type compiledReturnRouteRules struct {
	document      ReturnRouteRuleDocument
	asnGroups     map[string]map[int]struct{}
	prefixGroups  map[string][]*net.IPNet
	prefixRules   []returnRoutePrefixRule
	asnRuleCount  int
	cidrRuleCount int
}

type compiledReturnRouteBGPRules struct {
	document      ReturnRouteBGPRuleDocument
	prefixGroups  map[string][]*net.IPNet
	prefixRules   []returnRoutePrefixRule
	cidrRuleCount int
}

var returnRouteRuleState struct {
	once          sync.Once
	active        atomic.Pointer[compiledReturnRouteRules]
	builtin       *compiledReturnRouteRules
	base          *compiledReturnRouteRules
	bgp           *compiledReturnRouteBGPRules
	mu            sync.RWMutex
	status        ReturnRouteRuleStatus
	watcherCancel context.CancelFunc
	watcherDone   chan struct{}
	watchedDigest string
}

func ensureReturnRouteRules() {
	returnRouteRuleState.once.Do(func() {
		compiled, err := compileReturnRouteRules(builtinReturnRouteRuleJSON)
		if err != nil {
			panic(fmt.Sprintf("invalid embedded return route rules: %v", err))
		}
		returnRouteRuleState.builtin = compiled
		returnRouteRuleState.base = compiled
		returnRouteRuleState.active.Store(compiled)
		returnRouteRuleState.status = newReturnRouteRuleStatus(compiled, "builtin", false)
	})
}

func currentReturnRouteRules() *compiledReturnRouteRules {
	ensureReturnRouteRules()
	return returnRouteRuleState.active.Load()
}

func GetReturnRouteRules() ReturnRouteRuleView {
	ensureReturnRouteRules()
	returnRouteRuleState.mu.RLock()
	status := returnRouteRuleState.status
	document := cloneReturnRouteRuleDocument(returnRouteRuleState.base.document)
	returnRouteRuleState.mu.RUnlock()
	return ReturnRouteRuleView{Status: status, Rules: document}
}

func ReloadReturnRouteRules() (ReturnRouteRuleView, error) {
	ensureReturnRouteRules()
	now := time.Now().UTC()
	data, err := os.ReadFile(returnRouteRuleExternalPath)
	if errors.Is(err, os.ErrNotExist) {
		activateReturnRouteBaseRules(returnRouteRuleState.builtin, "builtin", now)
		return GetReturnRouteRules(), nil
	}
	if err != nil {
		recordReturnRouteRuleError(now, err)
		return GetReturnRouteRules(), err
	}
	if len(data) > returnRouteRuleMaxSize {
		err = fmt.Errorf("规则文件不能超过 %d MiB", returnRouteRuleMaxSize>>20)
		recordReturnRouteRuleError(now, err)
		return GetReturnRouteRules(), err
	}
	compiled, err := compileReturnRouteRules(data)
	if err != nil {
		recordReturnRouteRuleError(now, err)
		return GetReturnRouteRules(), err
	}
	activateReturnRouteBaseRules(compiled, "external", now)
	return GetReturnRouteRules(), nil
}

func UpdateReturnRouteRules(data []byte) (ReturnRouteRuleView, error) {
	ensureReturnRouteRules()
	if len(data) == 0 {
		return GetReturnRouteRules(), fmt.Errorf("规则文件不能为空")
	}
	if len(data) > returnRouteRuleMaxSize {
		return GetReturnRouteRules(), fmt.Errorf("规则文件不能超过 %d MiB", returnRouteRuleMaxSize>>20)
	}
	compiled, err := compileReturnRouteRules(data)
	if err != nil {
		return GetReturnRouteRules(), err
	}
	formatted, err := json.MarshalIndent(compiled.document, "", "  ")
	if err != nil {
		return GetReturnRouteRules(), fmt.Errorf("序列化规则文件失败: %w", err)
	}
	formatted = append(formatted, '\n')
	if err := writeReturnRouteRuleFileAtomically(returnRouteRuleExternalPath, ".return-route-signatures-*.json", formatted); err != nil {
		return GetReturnRouteRules(), err
	}
	activateReturnRouteBaseRules(compiled, "external", time.Now().UTC())
	return GetReturnRouteRules(), nil
}

func RefreshReturnRouteBGPRules(ctx context.Context) (ReturnRouteRuleView, error) {
	ensureReturnRouteRules()
	refreshCtx, cancel := context.WithTimeout(ctx, returnRouteBGPDownloadTimeout)
	defer cancel()
	if err := refreshReturnRouteBGPRules(refreshCtx); err != nil {
		return GetReturnRouteRules(), err
	}
	next := time.Now().UTC().Add(returnRouteBGPRefreshInterval)
	returnRouteRuleState.mu.Lock()
	returnRouteRuleState.status.BGPNextRefreshAt = &next
	returnRouteRuleState.mu.Unlock()
	return GetReturnRouteRules(), nil
}

func StartReturnRouteRuleWatcher() func() {
	ensureReturnRouteRules()
	if _, err := ReloadReturnRouteRules(); err != nil {
		logger.Errorf("return-route", "回程线路人工规则加载失败，继续使用上一版有效规则: %v", err)
	}
	if err := reloadCachedReturnRouteBGPRules(); err != nil {
		logger.Errorf("return-route", "回程线路 BGP 网段缓存加载失败，继续使用上一版有效规则: %v", err)
	}
	digest, _ := returnRouteRuleFilesDigest()

	returnRouteRuleState.mu.Lock()
	if returnRouteRuleState.watcherCancel != nil {
		returnRouteRuleState.mu.Unlock()
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	returnRouteRuleState.watcherCancel = cancel
	returnRouteRuleState.watcherDone = done
	returnRouteRuleState.watchedDigest = digest
	returnRouteRuleState.status.Watching = true
	returnRouteRuleState.mu.Unlock()

	go watchReturnRouteRules(ctx, done)
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			cancel()
			<-done
		})
	}
}

func watchReturnRouteRules(ctx context.Context, done chan struct{}) {
	defer func() {
		returnRouteRuleState.mu.Lock()
		if returnRouteRuleState.watcherDone == done {
			returnRouteRuleState.watcherCancel = nil
			returnRouteRuleState.watcherDone = nil
			returnRouteRuleState.status.Watching = false
		}
		returnRouteRuleState.mu.Unlock()
		close(done)
	}()
	fileTicker := time.NewTicker(returnRouteRulePollInterval)
	defer fileTicker.Stop()
	refreshTimer := time.NewTimer(0)
	defer refreshTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-fileTicker.C:
			reloadReturnRouteRuleFilesIfChanged()
		case <-refreshTimer.C:
			refreshCtx, cancel := context.WithTimeout(ctx, returnRouteBGPDownloadTimeout)
			if err := refreshReturnRouteBGPRules(refreshCtx); err != nil && ctx.Err() == nil {
				logger.Errorf("return-route", "回程线路 BGP 网段更新失败，继续使用上一版有效规则: %v", err)
			}
			cancel()
			next := time.Now().UTC().Add(returnRouteBGPRefreshInterval)
			returnRouteRuleState.mu.Lock()
			returnRouteRuleState.status.BGPNextRefreshAt = &next
			returnRouteRuleState.mu.Unlock()
			refreshTimer.Reset(returnRouteBGPRefreshInterval)
		}
	}
}

func reloadReturnRouteRuleFilesIfChanged() {
	digest, err := returnRouteRuleFilesDigest()
	if err != nil {
		recordReturnRouteRuleError(time.Now().UTC(), err)
		return
	}
	returnRouteRuleState.mu.Lock()
	changed := digest != returnRouteRuleState.watchedDigest
	if changed {
		returnRouteRuleState.watchedDigest = digest
	}
	returnRouteRuleState.mu.Unlock()
	if !changed {
		return
	}
	if _, err := ReloadReturnRouteRules(); err != nil {
		logger.Errorf("return-route", "回程线路人工规则热加载失败，继续使用上一版有效规则: %v", err)
	}
	if err := reloadCachedReturnRouteBGPRules(); err != nil {
		logger.Errorf("return-route", "回程线路 BGP 网段热加载失败，继续使用上一版有效规则: %v", err)
	}
}

func refreshReturnRouteBGPRules(ctx context.Context) error {
	url := returnRouteBGPSourceURL()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		recordReturnRouteBGPError(err)
		return err
	}
	request.Header.Set("User-Agent", "Komari-Return-Route (+https://github.com/nuomiiiii/komari)")
	response, err := returnRouteBGPHTTPClient.Do(request)
	if err != nil {
		recordReturnRouteBGPError(err)
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		err = fmt.Errorf("BGP 规则源返回 HTTP %d", response.StatusCode)
		recordReturnRouteBGPError(err)
		return err
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, returnRouteBGPRuleMaxSize+1))
	if err != nil {
		recordReturnRouteBGPError(err)
		return err
	}
	if len(data) > returnRouteBGPRuleMaxSize {
		err = fmt.Errorf("BGP 规则文件不能超过 %d MiB", returnRouteBGPRuleMaxSize>>20)
		recordReturnRouteBGPError(err)
		return err
	}
	compiled, err := compileReturnRouteBGPRules(data)
	if err != nil {
		recordReturnRouteBGPError(err)
		return err
	}
	formatted, err := json.MarshalIndent(compiled.document, "", "  ")
	if err != nil {
		recordReturnRouteBGPError(err)
		return err
	}
	formatted = append(formatted, '\n')
	if err := writeReturnRouteRuleFileAtomically(returnRouteBGPExternalPath, ".return-route-bgp-prefixes-*.json", formatted); err != nil {
		recordReturnRouteBGPError(err)
		return err
	}
	activateReturnRouteBGPRules(compiled, time.Now().UTC())
	return nil
}

func reloadCachedReturnRouteBGPRules() error {
	data, err := os.ReadFile(returnRouteBGPExternalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		recordReturnRouteBGPError(err)
		return err
	}
	if len(data) > returnRouteBGPRuleMaxSize {
		err = fmt.Errorf("BGP 规则文件不能超过 %d MiB", returnRouteBGPRuleMaxSize>>20)
		recordReturnRouteBGPError(err)
		return err
	}
	compiled, err := compileReturnRouteBGPRules(data)
	if err != nil {
		recordReturnRouteBGPError(err)
		return err
	}
	activateReturnRouteBGPRules(compiled, time.Now().UTC())
	return nil
}

func compileReturnRouteRules(data []byte) (*compiledReturnRouteRules, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document ReturnRouteRuleDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("解析规则文件失败: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if document.SchemaVersion != returnRouteRuleSchemaVersion {
		return nil, fmt.Errorf("不支持的 schema_version %d，当前仅支持 %d", document.SchemaVersion, returnRouteRuleSchemaVersion)
	}
	document.RuleVersion = strings.TrimSpace(document.RuleVersion)
	if document.RuleVersion == "" {
		return nil, fmt.Errorf("rule_version 不能为空")
	}
	if err := validateReturnRouteRuleKeys("asn_groups", document.ASNGroups, requiredReturnRouteASNGroups); err != nil {
		return nil, err
	}
	if err := validateReturnRouteRuleKeys("prefix_groups", document.PrefixGroups, requiredReturnRouteASNGroups); err != nil {
		return nil, err
	}
	if err := validateReturnRouteRuleKeys("confidence", document.Confidence, requiredReturnRouteConfidence); err != nil {
		return nil, err
	}

	compiled := &compiledReturnRouteRules{
		document:     cloneReturnRouteRuleDocument(document),
		asnGroups:    make(map[string]map[int]struct{}, len(document.ASNGroups)),
		prefixGroups: make(map[string][]*net.IPNet, len(document.PrefixGroups)),
	}
	seenASNs := make(map[int]string)
	for _, group := range requiredReturnRouteASNGroups {
		compiled.asnGroups[group] = make(map[int]struct{}, len(document.ASNGroups[group]))
		for _, asn := range document.ASNGroups[group] {
			if asn <= 0 {
				return nil, fmt.Errorf("asn_groups.%s 包含无效 ASN %d", group, asn)
			}
			if previous, ok := seenASNs[asn]; ok {
				return nil, fmt.Errorf("ASN %d 同时出现在 %s 和 %s", asn, previous, group)
			}
			seenASNs[asn] = group
			compiled.asnGroups[group][asn] = struct{}{}
			compiled.asnRuleCount++
		}
	}
	seenPrefixes := make(map[string]string)
	for _, group := range requiredReturnRouteASNGroups {
		for _, value := range document.PrefixGroups[group] {
			network, normalized, err := parseReturnRouteCIDR(value)
			if err != nil {
				return nil, fmt.Errorf("prefix_groups.%s 包含无效网段 %q", group, value)
			}
			if previous, ok := seenPrefixes[normalized]; ok {
				return nil, fmt.Errorf("网段 %s 同时出现在 %s 和 %s", normalized, previous, group)
			}
			seenPrefixes[normalized] = group
			compiled.prefixGroups[group] = append(compiled.prefixGroups[group], network)
			compiled.prefixRules = append(compiled.prefixRules, returnRoutePrefixRule{group: group, network: network})
			compiled.cidrRuleCount++
		}
	}
	for _, key := range requiredReturnRouteConfidence {
		value := document.Confidence[key]
		if value <= 0 || value > 1 {
			return nil, fmt.Errorf("confidence.%s 必须大于 0 且不超过 1", key)
		}
	}
	return compiled, nil
}

func compileReturnRouteBGPRules(data []byte) (*compiledReturnRouteBGPRules, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document ReturnRouteBGPRuleDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("解析 BGP 规则文件失败: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if document.SchemaVersion != returnRouteRuleSchemaVersion {
		return nil, fmt.Errorf("不支持的 BGP schema_version %d", document.SchemaVersion)
	}
	if document.GeneratedAt.IsZero() {
		return nil, fmt.Errorf("BGP 规则 generated_at 不能为空")
	}
	document.Source = strings.TrimSpace(document.Source)
	if document.Source == "" {
		return nil, fmt.Errorf("BGP 规则 source 不能为空")
	}
	if err := validateReturnRouteRuleKeys("prefix_groups", document.PrefixGroups, requiredReturnRouteASNGroups); err != nil {
		return nil, err
	}
	compiled := &compiledReturnRouteBGPRules{
		document: ReturnRouteBGPRuleDocument{
			SchemaVersion: document.SchemaVersion,
			GeneratedAt:   document.GeneratedAt.UTC(),
			Source:        document.Source,
			PrefixGroups:  make(map[string][]string, len(document.PrefixGroups)),
		},
		prefixGroups: make(map[string][]*net.IPNet, len(document.PrefixGroups)),
	}
	seen := make(map[string]string)
	for _, group := range requiredReturnRouteASNGroups {
		for _, value := range document.PrefixGroups[group] {
			network, normalized, err := parseReturnRouteCIDR(value)
			if err != nil {
				return nil, fmt.Errorf("prefix_groups.%s 包含无效网段 %q", group, value)
			}
			if previous, ok := seen[normalized]; ok {
				return nil, fmt.Errorf("BGP 网段 %s 同时出现在 %s 和 %s", normalized, previous, group)
			}
			seen[normalized] = group
			compiled.document.PrefixGroups[group] = append(compiled.document.PrefixGroups[group], normalized)
			compiled.prefixGroups[group] = append(compiled.prefixGroups[group], network)
			compiled.prefixRules = append(compiled.prefixRules, returnRoutePrefixRule{group: group, network: network})
			compiled.cidrRuleCount++
		}
	}
	return compiled, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("解析规则文件失败: %w", err)
	}
	return fmt.Errorf("规则文件只能包含一个 JSON 对象")
}

func validateReturnRouteRuleKeys[T any](name string, values map[string]T, required []string) error {
	if values == nil {
		return fmt.Errorf("%s 不能为空", name)
	}
	known := make(map[string]struct{}, len(required))
	for _, key := range required {
		known[key] = struct{}{}
		if _, ok := values[key]; !ok {
			return fmt.Errorf("%s 缺少必需分组 %q", name, key)
		}
	}
	for key := range values {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("%s 包含未知分组 %q", name, key)
		}
	}
	return nil
}

func parseReturnRouteCIDR(value string) (*net.IPNet, string, error) {
	_, network, err := net.ParseCIDR(strings.TrimSpace(value))
	if err != nil {
		return nil, "", err
	}
	return network, network.String(), nil
}

func (rules *compiledReturnRouteRules) hasASN(group string, asn int) bool {
	_, ok := rules.asnGroups[group][asn]
	return ok
}

func (rules *compiledReturnRouteRules) hasPrefix(group, value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return false
	}
	bestGroup := ""
	bestMask := -1
	for _, rule := range rules.prefixRules {
		if !rule.network.Contains(ip) {
			continue
		}
		mask, _ := rule.network.Mask.Size()
		if mask > bestMask {
			bestGroup = rule.group
			bestMask = mask
		}
	}
	return bestGroup == group
}

func (rules *compiledReturnRouteRules) hasSignature(group string, hop returnRouteSignature) bool {
	return rules.hasASN(group, hop.asn) || rules.hasPrefix(group, hop.ip)
}

func (rules *compiledReturnRouteRules) representativeASN(group string) int {
	values := rules.document.ASNGroups[group]
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func localReturnRouteASN(value string, rules *compiledReturnRouteRules) int {
	for _, group := range requiredReturnRouteASNGroups {
		if rules.hasPrefix(group, value) {
			return rules.representativeASN(group)
		}
	}
	return 0
}

func activateReturnRouteBaseRules(compiled *compiledReturnRouteRules, source string, now time.Time) {
	returnRouteRuleState.mu.Lock()
	returnRouteRuleState.base = compiled
	active := mergeReturnRouteRules(compiled, returnRouteRuleState.bgp)
	returnRouteRuleState.active.Store(active)
	watching := returnRouteRuleState.status.Watching
	bgpGeneratedAt := returnRouteRuleState.status.BGPGeneratedAt
	bgpLoadedAt := returnRouteRuleState.status.BGPLoadedAt
	bgpNextRefreshAt := returnRouteRuleState.status.BGPNextRefreshAt
	bgpLastError := returnRouteRuleState.status.BGPLastError
	returnRouteRuleState.status = newReturnRouteRuleStatus(compiled, source, watching)
	returnRouteRuleState.status.LoadedAt = now
	returnRouteRuleState.status.LastAttemptAt = now
	returnRouteRuleState.status.BGPCIDRCount = active.cidrRuleCount - compiled.cidrRuleCount
	returnRouteRuleState.status.CIDRRuleCount = active.cidrRuleCount
	returnRouteRuleState.status.BGPGeneratedAt = bgpGeneratedAt
	returnRouteRuleState.status.BGPLoadedAt = bgpLoadedAt
	returnRouteRuleState.status.BGPNextRefreshAt = bgpNextRefreshAt
	returnRouteRuleState.status.BGPLastError = bgpLastError
	returnRouteRuleState.mu.Unlock()
}

func activateReturnRouteBGPRules(compiled *compiledReturnRouteBGPRules, now time.Time) {
	returnRouteRuleState.mu.Lock()
	returnRouteRuleState.bgp = compiled
	active := mergeReturnRouteRules(returnRouteRuleState.base, compiled)
	returnRouteRuleState.active.Store(active)
	generatedAt := compiled.document.GeneratedAt.UTC()
	returnRouteRuleState.status.BGPGeneratedAt = &generatedAt
	returnRouteRuleState.status.BGPLoadedAt = &now
	returnRouteRuleState.status.BGPCIDRCount = active.cidrRuleCount - returnRouteRuleState.base.cidrRuleCount
	returnRouteRuleState.status.CIDRRuleCount = active.cidrRuleCount
	returnRouteRuleState.status.BGPLastError = ""
	returnRouteRuleState.mu.Unlock()
}

func mergeReturnRouteRules(base *compiledReturnRouteRules, bgp *compiledReturnRouteBGPRules) *compiledReturnRouteRules {
	if bgp == nil {
		return base
	}
	merged := &compiledReturnRouteRules{
		document:      cloneReturnRouteRuleDocument(base.document),
		asnGroups:     base.asnGroups,
		prefixGroups:  make(map[string][]*net.IPNet, len(base.prefixGroups)),
		prefixRules:   append([]returnRoutePrefixRule(nil), base.prefixRules...),
		asnRuleCount:  base.asnRuleCount,
		cidrRuleCount: base.cidrRuleCount,
	}
	seen := make(map[string]struct{}, base.cidrRuleCount)
	for _, rule := range base.prefixRules {
		seen[rule.network.String()] = struct{}{}
	}
	for group, networks := range base.prefixGroups {
		merged.prefixGroups[group] = append([]*net.IPNet(nil), networks...)
	}
	for _, group := range requiredReturnRouteASNGroups {
		for index, network := range bgp.prefixGroups[group] {
			if _, ok := seen[network.String()]; ok {
				continue
			}
			seen[network.String()] = struct{}{}
			merged.prefixGroups[group] = append(merged.prefixGroups[group], network)
			merged.prefixRules = append(merged.prefixRules, returnRoutePrefixRule{group: group, network: network})
			merged.document.PrefixGroups[group] = append(merged.document.PrefixGroups[group], bgp.document.PrefixGroups[group][index])
			merged.cidrRuleCount++
		}
	}
	return merged
}

func newReturnRouteRuleStatus(compiled *compiledReturnRouteRules, source string, watching bool) ReturnRouteRuleStatus {
	now := time.Now().UTC()
	return ReturnRouteRuleStatus{
		Source:          source,
		RuleVersion:     compiled.document.RuleVersion,
		SchemaVersion:   compiled.document.SchemaVersion,
		LoadedAt:        now,
		LastAttemptAt:   now,
		ExternalPath:    returnRouteRuleExternalPath,
		ASNRuleCount:    compiled.asnRuleCount,
		ManualCIDRCount: compiled.cidrRuleCount,
		CIDRRuleCount:   compiled.cidrRuleCount,
		Watching:        watching,
		BGPSourceURL:    returnRouteBGPSourceURL(),
	}
}

func recordReturnRouteRuleError(now time.Time, err error) {
	returnRouteRuleState.mu.Lock()
	returnRouteRuleState.status.LastAttemptAt = now
	returnRouteRuleState.status.LastError = err.Error()
	returnRouteRuleState.mu.Unlock()
}

func recordReturnRouteBGPError(err error) {
	returnRouteRuleState.mu.Lock()
	returnRouteRuleState.status.BGPLastError = err.Error()
	returnRouteRuleState.mu.Unlock()
}

func writeReturnRouteRuleFileAtomically(path, pattern string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("创建规则目录失败: %w", err)
	}
	temporary, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return fmt.Errorf("创建临时规则文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0644); err != nil {
		temporary.Close()
		return fmt.Errorf("设置规则文件权限失败: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("写入规则文件失败: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("同步规则文件失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭规则文件失败: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("替换规则文件失败: %w", err)
	}
	return nil
}

func returnRouteRuleFilesDigest() (string, error) {
	baseDigest, err := returnRouteRuleFileDigest(returnRouteRuleExternalPath)
	if err != nil {
		return "", err
	}
	bgpDigest, err := returnRouteRuleFileDigest(returnRouteBGPExternalPath)
	if err != nil {
		return "", err
	}
	return baseDigest + ":" + bgpDigest, nil
}

func returnRouteRuleFileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

func returnRouteBGPSourceURL() string {
	if value := strings.TrimSpace(os.Getenv("KOMARI_RETURN_ROUTE_BGP_RULE_URL")); value != "" {
		return value
	}
	return returnRouteBGPDefaultURL
}

func cloneReturnRouteRuleDocument(document ReturnRouteRuleDocument) ReturnRouteRuleDocument {
	cloned := ReturnRouteRuleDocument{
		SchemaVersion: document.SchemaVersion,
		RuleVersion:   document.RuleVersion,
		ASNGroups:     make(map[string][]int, len(document.ASNGroups)),
		PrefixGroups:  make(map[string][]string, len(document.PrefixGroups)),
		Confidence:    make(map[string]float64, len(document.Confidence)),
	}
	for key, values := range document.ASNGroups {
		cloned.ASNGroups[key] = append([]int(nil), values...)
	}
	for key, values := range document.PrefixGroups {
		cloned.PrefixGroups[key] = append([]string(nil), values...)
	}
	for key, value := range document.Confidence {
		cloned.Confidence[key] = value
	}
	return cloned
}
