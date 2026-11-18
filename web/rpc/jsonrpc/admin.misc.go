package jsonrpc

import (
	"context"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/records"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/metric"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/pkg/themehttp"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/utils/messageSender"
	agent "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/mcp"
	"github.com/nuomiiiii/lite/web/passkey"
	"github.com/nuomiiiii/lite/web/remotectl"
	"github.com/nuomiiiii/lite/web/scheduledexec"
)

// admin.misc.go
// 杂项 admin RPC2 方法：会话管理、设置、客户端排序。

func parseUintKey(s string) (uint, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	return uint(v), err
}

func init() {
	RegisterWithGroupAndMeta("getSessions", rpc.RoleAdmin, adminGetSessions, &rpc.MethodMeta{
		Name:    "admin:getSessions",
		Summary: "List all login sessions",
		Returns: "{ current: string, data: Session[] }",
	})
	RegisterWithGroupAndMeta("deleteSession", rpc.RoleAdmin, adminDeleteSession, &rpc.MethodMeta{
		Name:    "admin:deleteSession",
		Summary: "Delete a session by token",
		Returns: "null",
	})
	RegisterWithGroupAndMeta("deleteAllSessions", rpc.RoleAdmin, adminDeleteAllSessions, &rpc.MethodMeta{
		Name:    "admin:deleteAllSessions",
		Summary: "Delete all sessions",
		Returns: "null",
	})
	RegisterWithGroupAndMeta("getSettings", rpc.RoleAdmin, adminGetSettings, &rpc.MethodMeta{
		Name:    "admin:getSettings",
		Summary: "Get all settings",
		Returns: "object",
	})
	RegisterWithGroupAndMeta("editSettings", rpc.RoleAdmin, adminEditSettings, &rpc.MethodMeta{
		Name:    "admin:editSettings",
		Summary: "Update settings (partial)",
		Returns: "null",
	})
	RegisterWithGroupAndMeta("clearAllRecords", rpc.RoleAdmin, adminClearAllRecords, &rpc.MethodMeta{
		Name:    "admin:clearAllRecords",
		Summary: "Delete all load and ping records",
		Returns: "null",
	})
	RegisterWithGroupAndMeta("orderClients", rpc.RoleAdmin, adminOrderClients, &rpc.MethodMeta{
		Name:    "admin:orderClients",
		Summary: "Reorder clients (map of uuid->weight)",
		Returns: "null",
	})
}

func adminGetSessions(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	ss, err := accounts.GetAllSessions()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to retrieve sessions: "+err.Error(), nil)
	}
	now := time.Now().UTC()
	ttl := accounts.SessionTTL()
	for i := range ss {
		ss[i] = accounts.SessionForDisplay(ss[i], now, ttl)
	}
	current := ""
	if meta := rpc.MetaFromContext(ctx); meta != nil && meta.SessionToken != "" {
		current = accounts.SessionLookupKey(meta.SessionToken)
	}
	return map[string]any{"current": current, "data": ss, "server_time": now}, nil
}

func adminDeleteSession(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	var params struct {
		Session string `json:"session"`
	}
	req.BindParams(&params)
	if params.Session == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "session is required", nil)
	}
	if err := accounts.DeleteSession(params.Session); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to delete session: "+err.Error(), nil)
	}
	remotectl.RevokeLogin(params.Session)
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "info", "audit.session_delete", nil)
	return nil, nil
}

func adminDeleteAllSessions(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	if err := accounts.DeleteAllSessions(); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to delete all sessions: "+err.Error(), nil)
	}
	remotectl.RevokeAll()
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "warn", "audit.session_delete_all", nil)
	return nil, nil
}

func adminGetSettings(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := messageSender.EnsureRoutes(); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to prepare notification routes: "+err.Error(), nil)
	}
	cst, err := config.GetAll()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get settings: "+err.Error(), nil)
	}
	stripRetiredAdminSettings(cst)
	if meta := rpc.MetaFromContext(ctx); meta != nil && meta.Principal != nil && (meta.Principal.IsAPIKey || meta.Principal.Type == rpc.PrincipalAPIKey) {
		cst[config.ApiKeyKey] = ""
	}
	return cst, nil
}

func stripRetiredAdminSettings(cst map[string]any) {
	delete(cst, config.CloudflareTunnelTokenKey)
	delete(cst, config.LowResourceModeKey)
	delete(cst, config.SiteFactoryDefaultsKey)
	delete(cst, metricstore.MetricDownsamplingEnabledKey)
	delete(cst, config.AutoDiscoveryKeyKey)
}

// metricStoreConfigKeys 是与 metrics 独立数据库相关、需要触发连接测试 + 热重载的配置键。
//
// 注意：metric_store_enabled 已废弃（metric store 始终启用），不再纳入此集合。
var metricStoreConfigKeys = map[string]struct{}{
	"metric_db_type":                  {},
	metricstore.MetricDBDriverKey:     {},
	metricstore.MetricDBDSNKey:        {},
	metricstore.MetricTablePrefixKey:  {},
	metricstore.MetricMaxOpenConnsKey: {},
	metricstore.MetricMaxIdleConnsKey: {},
}

// metricKeysTouched 判断本次设置变更是否涉及 metrics 数据库相关键。
func metricKeysTouched(cfg map[string]interface{}) bool {
	for key := range cfg {
		if _, ok := metricStoreConfigKeys[key]; ok {
			return true
		}
	}
	return false
}

func adminEditSettings(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	cfg := make(map[string]interface{})
	if err := req.BindParams(&cfg); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid or missing request body: "+err.Error(), nil)
	}
	if settingsRequireHumanSession(cfg) {
		if err := denyAPIKey(ctx); err != nil {
			return nil, err
		}
	}
	if rawTime, ok := cfg[config.TrafficReportTimeKey]; ok {
		reportTime, ok := rawTime.(string)
		if !ok {
			return nil, rpc.MakeError(rpc.InvalidParams, "Traffic report time must be a string", nil)
		}
		normalized, err := config.NormalizeTrafficReportTime(reportTime)
		if err != nil {
			return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
		}
		cfg[config.TrafficReportTimeKey] = normalized
	}
	if rawPageSize, ok := cfg[config.AdminDefaultPageSizeKey]; ok {
		pageSize, ok := normalizeAdminDefaultPageSize(rawPageSize)
		if !ok {
			return nil, rpc.MakeError(
				rpc.InvalidParams,
				"Admin default page size must be an integer between "+
					strconv.Itoa(config.AdminDefaultPageSizeMin)+" and "+
					strconv.Itoa(config.AdminDefaultPageSizeMax),
				nil,
			)
		}
		cfg[config.AdminDefaultPageSizeKey] = pageSize
	}
	if rawStep, ok := cfg[config.TrafficReminderStepKey]; ok {
		step, ok := normalizeTrafficReminderStep(rawStep)
		if !ok {
			return nil, rpc.MakeError(rpc.InvalidParams, "Traffic reminder step must be an integer between 1 and 100", nil)
		}
		cfg[config.TrafficReminderStepKey] = step
	}
	if rawRoutes, ok := cfg[config.NotificationRoutesKey]; ok {
		routes, err := messageSender.NormalizeRoutes(rawRoutes)
		if err != nil {
			return nil, rpc.MakeError(rpc.InvalidParams, "Invalid notification routes: "+err.Error(), nil)
		}
		cfg[config.NotificationRoutesKey] = routes
		cfg[config.NotificationRoutesMigratedKey] = true
	}
	if rawDigest, ok := cfg[config.NotificationDigestSecondsKey]; ok {
		seconds, err := messageSender.NormalizeDigestSeconds(rawDigest)
		if err != nil {
			return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
		}
		cfg[config.NotificationDigestSecondsKey] = seconds
	}
	if rawTTL, ok := cfg[config.SessionTTLSecondsKey]; ok {
		ttl, ok := normalizeSessionTTLSeconds(rawTTL)
		if !ok {
			return nil, rpc.MakeError(
				rpc.InvalidParams,
				"Session TTL must be an integer between 60 and 2592000 seconds",
				nil,
			)
		}
		cfg[config.SessionTTLSecondsKey] = ttl
	}
	if rawProxy, ok := cfg[config.GitHubProxyKey]; ok {
		normalized, err := normalizeGitHubProxySetting(rawProxy)
		if err != nil {
			return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
		}
		cfg[config.GitHubProxyKey] = normalized
	}
	// Ignore retired controls submitted by an older cached frontend. The
	// startup normalizer keeps their persisted compatibility values fixed.
	delete(cfg, config.LowResourceModeKey)
	delete(cfg, config.SiteFactoryDefaultsKey)
	delete(cfg, config.ReduceMotionKey)
	delete(cfg, metricstore.MetricDownsamplingEnabledKey)
	delete(cfg, config.AutoDiscoveryKeyKey)

	previousRemote, _ := config.GetAs[bool](config.AllowRemoteManagementKey, false)
	previousMCP, _ := config.GetAs[bool](config.AllowMCPKey, false)

	touchedMetric := metricKeysTouched(cfg)
	if touchedMetric {
		// 数据库类型不再由前端显式选择，而是根据 DSN 自动推断后写回配置，
		// 使后续连接测试、热重载和初始化都使用一致的 driver。
		if v, ok := cfg[metricstore.MetricDBDSNKey]; ok {
			if dsn, ok := v.(string); ok {
				dsn = strings.TrimSpace(dsn)
				cfg[metricstore.MetricDBDSNKey] = dsn
				if driver, inferred := metricstore.InferDriverFromDSN(dsn); inferred {
					cfg[metricstore.MetricDBDriverKey] = string(driver)
				}
			}
		}

		merged, err := mergedMetricConfig(cfg)
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to resolve metric store config: "+err.Error(), nil)
		}
		if err := validateMetricDBDSN(merged.Driver, merged.DSN); err != nil {
			return nil, rpc.MakeError(rpc.InvalidParams, "Invalid metric_db_dsn: "+err.Error(), nil)
		}
		testCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := metricstore.TestConnection(testCtx, merged); err != nil {
			cancel()
			return nil, rpc.MakeError(rpc.InvalidParams,
				"Metrics database connection test failed: "+err.Error(), nil)
		}
		cancel()
	}

	if closed, err := closingLastSignInMethod(ctx, cfg); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to read sign-in settings: "+err.Error(), nil)
	} else if closed {
		return nil, rpc.MakeError(rpc.InvalidParams, "At least one sign-in method must stay enabled", nil)
	}

	previousSettings, settingsErr := config.GetAll()
	if settingsErr != nil {
		previousSettings = map[string]any{}
	}
	if err := config.SetMany(cfg); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to update settings: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	for _, change := range auditlog.SettingChanges(previousSettings, cfg) {
		auditlog.Event(ip, actor, "info", change.Key, change.Params)
	}
	if _, ok := cfg[config.SessionTTLSecondsKey]; ok {
		if err := accounts.CapLegacySessionExpires(accounts.SessionTTL()); err != nil {
			logger.Errorf("admin", "failed to cap session expiry after TTL update: %v", err)
			return nil, rpc.MakeError(rpc.InternalError, "Failed to apply session timeout: "+err.Error(), nil)
		}
		if meta := rpc.MetaFromContext(ctx); meta != nil && meta.SessionToken != "" {
			if _, _, err := accounts.TouchSession(meta.SessionToken, meta.UserAgent, meta.RemoteIP); err != nil {
				logger.Errorf("admin", "failed to refresh current session after TTL update: %v", err)
				return nil, rpc.MakeError(rpc.InternalError, "Failed to refresh current session: "+err.Error(), nil)
			}
		}
	}
	if raw, ok := cfg[config.AllowRemoteManagementKey]; ok && previousRemote && !toBool(raw, false) {
		removed := agent.DrainRemoteDelivery(func() []agent.RemovedV2Event {
			remotectl.RevokeAll()
			return agent.RemoveAllV2EventsByMethods(
				v2.MethodAgentRemote,
				v2.MethodAgentExec,
				v2.MethodAgentMCPExec,
				v2.MethodAgentMCPFile,
				v2.MethodAgentMCPCancel,
				v2.MethodAgentMCPRenew,
				v2.MethodAgentMCPRevoke,
			)
		})
		cancelErr := cancelUndeliveredRemoteExec(removed)
		stopErr := scheduledexec.StopForRemoteOff()
		if cancelErr != nil && stopErr != nil {
			return nil, rpc.MakeError(rpc.InternalError, "远程管理已关闭，但未能写入已取消任务结果: "+cancelErr.Error()+"；未能停用定时任务: "+stopErr.Error(), nil)
		}
		if cancelErr != nil {
			return nil, rpc.MakeError(rpc.InternalError, "远程管理已关闭，但未能写入已取消任务结果: "+cancelErr.Error(), nil)
		}
		if stopErr != nil {
			return nil, rpc.MakeError(rpc.InternalError, "远程管理已关闭，但未能停用定时任务: "+stopErr.Error(), nil)
		}
	}
	if raw, ok := cfg[config.AllowMCPKey]; ok && previousMCP && !toBool(raw, false) {
		mcp.DrainMCPDelivery()
	}
	// 配置已落库，热重载 metric store（无需重启）。连接已在上面验证过，
	// 这里再次失败属异常情况，回报给用户。
	if touchedMetric {
		reloadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := metricstore.Reload(reloadCtx); err != nil {
			cancel()
			return nil, rpc.MakeError(rpc.InternalError,
				"Settings saved but metrics database hot reload failed: "+err.Error(), nil)
		}
		cancel()
	}

	return nil, nil
}

func normalizeAdminDefaultPageSize(raw any) (int, bool) {
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		value < config.AdminDefaultPageSizeMin || value > config.AdminDefaultPageSizeMax {
		return 0, false
	}
	return int(value), true
}

func normalizeGitHubProxySetting(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", errors.New("GitHub 代理地址必须是字符串")
	}
	normalized, err := themehttp.NormalizeGitHubProxy(value)
	if err == nil {
		return normalized, nil
	}
	switch {
	case errors.Is(err, themehttp.ErrPrivateAddress):
		return "", errors.New("代理地址不能是私有或保留地址")
	case errors.Is(err, themehttp.ErrUnsupportedScheme):
		return "", errors.New("代理地址仅支持 HTTP 或 HTTPS")
	default:
		return "", errors.New("代理地址无效")
	}
}

func normalizeTrafficReminderStep(raw any) (int, bool) {
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, false
	}
	step := int(value)
	if step < 1 || step > 100 {
		return 0, false
	}
	return step, true
}

func normalizeSessionTTLSeconds(raw any) (int, bool) {
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, false
	}
	seconds := int(value)
	if seconds < accounts.MinSessionTTLSeconds || seconds > accounts.MaxSessionTTLSeconds {
		return 0, false
	}
	return seconds, true
}

// mergedMetricConfig 读取当前持久化的 metric store 配置，并把本次请求中涉及的
// metrics 相关键覆盖上去，得到「即将生效」的目标配置，用于落库前的连接测试。
func mergedMetricConfig(cfg map[string]interface{}) (*metricstore.MetricStoreConfig, error) {
	merged, err := config.GetManyAs[metricstore.MetricStoreConfig]()
	if err != nil {
		return nil, err
	}

	if v, ok := cfg["metric_db_type"]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			merged.Driver = strings.TrimSpace(s)
		}
	}
	if v, ok := cfg[metricstore.MetricDBDriverKey]; ok {
		if s, ok := v.(string); ok {
			merged.Driver = s
		}
	}

	if v, ok := cfg[metricstore.MetricDBDSNKey]; ok {
		if s, ok := v.(string); ok {
			merged.DSN = s
		}
	}
	if v, ok := cfg[metricstore.MetricTablePrefixKey]; ok {
		if s, ok := v.(string); ok {
			merged.TablePrefix = s
		}
	}
	if v, ok := cfg[metricstore.MetricMaxOpenConnsKey]; ok {
		merged.MaxOpenConns = toInt(v, merged.MaxOpenConns)
	}
	if v, ok := cfg[metricstore.MetricMaxIdleConnsKey]; ok {
		merged.MaxIdleConns = toInt(v, merged.MaxIdleConns)
	}

	return merged, nil
}

func signInMethodsAllClosed(avail passkey.SignInAvailability, cfg map[string]any) bool {
	_, touchPassword := cfg[config.DisablePasswordLoginKey]
	_, touchOAuth := cfg[config.OAuthEnabledKey]
	if !touchPassword && !touchOAuth {
		return false
	}
	if touchPassword {
		avail.PasswordDisabled = toBool(cfg[config.DisablePasswordLoginKey], avail.PasswordDisabled)
	}
	if touchOAuth {
		avail.OAuthEnabled = toBool(cfg[config.OAuthEnabledKey], avail.OAuthEnabled)
	}
	return avail.Remaining() == 0
}

func closingLastSignInMethod(ctx context.Context, cfg map[string]any) (bool, error) {
	if _, ok := cfg[config.DisablePasswordLoginKey]; !ok {
		if _, ok := cfg[config.OAuthEnabledKey]; !ok {
			return false, nil
		}
	}
	meta := rpc.MetaFromContext(ctx)
	if meta == nil || meta.UserUUID == "" {
		return false, errors.New("missing signed-in account")
	}
	avail, err := passkey.CurrentSignInAvailability(meta.UserUUID)
	if err != nil {
		return false, err
	}
	return signInMethodsAllClosed(avail, cfg), nil
}

func toBool(v any, fallback bool) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		if parsed, err := strconv.ParseBool(val); err == nil {
			return parsed
		}
	}
	return fallback
}

// toInt 将 JSON 解码得到的任意值（通常是 float64 或 string）转换为 int，失败时返回 fallback。
func toInt(v any, fallback int) int {

	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	case string:
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return fallback
}

var cancelUndeliveredTaskResult = tasks.CancelUndeliveredTaskResult

func cancelUndeliveredRemoteExec(removed []agent.RemovedV2Event) error {
	var errs []error
	for _, item := range removed {
		if item.Event.Method != v2.MethodAgentExec {
			continue
		}
		if item.Event.HandedOff {
			continue
		}
		taskID := agent.ExecTaskID(item.Event)
		if taskID == "" {
			continue
		}
		if err := cancelUndeliveredTaskResult(taskID, item.UUID, v2.RemoteManagementClosedTaskResult); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cancelExecTaskClients(taskID string, clients []string) error {
	var errs []error
	for _, uuid := range clients {
		if uuid == "" {
			continue
		}
		if err := cancelUndeliveredTaskResult(taskID, uuid, v2.RemoteManagementClosedTaskResult); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func adminClearAllRecords(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {

	records.DeleteAll()
	tasks.DeleteAllPingRecords()
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "info", "audit.records_clear_all", nil)
	return nil, nil
}

func evalSymlinksClosest(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	cur := abs
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(abs), nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func extractSQLiteFilePath(raw string) string {
	p := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(p), "file:") {
		p = p[5:]
	}
	if idx := strings.IndexByte(p, '#'); idx >= 0 {
		p = p[:idx]
	}
	if idx := strings.IndexByte(p, '?'); idx >= 0 {
		q := strings.ToLower(p[idx+1:])
		if strings.Contains(q, "mode=memory") {
			return ""
		}
		p = p[:idx]
	}
	p = strings.TrimSpace(p)
	if p == "" || p == ":memory:" {
		return ""
	}
	return p
}

func forbiddenSQLiteTargets(workingDir, realDataDir string) []string {
	candidates := []string{
		filepath.Join(realDataDir, "lite.db"),
		filepath.Join(realDataDir, "lite-instance.key"),
	}
	if mainPath := extractSQLiteFilePath(flags.DatabaseFile); mainPath != "" {
		if !filepath.IsAbs(mainPath) {
			mainPath = filepath.Join(workingDir, mainPath)
		}
		if resolvedMain, err := evalSymlinksClosest(mainPath); err == nil {
			candidates = append(candidates, resolvedMain)
		}
	}
	out := make([]string, 0, len(candidates)*5)
	for _, base := range candidates {
		base = filepath.Clean(base)
		out = append(out, base)
		if !strings.EqualFold(filepath.Base(base), "lite-instance.key") {
			out = append(out, base+"-wal", base+"-shm", base+"-journal", base+".restore")
		}
	}
	return out
}

func validateMetricDBDSN(driver, dsn string) error {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil
	}
	decoded := dsn
	for i := 0; i < 5; i++ {
		if strings.Contains(strings.ToLower(decoded), "allowallfiles") {
			return errors.New("allowAllFiles is not permitted in metric_db_dsn")
		}
		next, err := url.QueryUnescape(decoded)
		if err != nil || next == decoded {
			break
		}
		decoded = next
	}
	if strings.Contains(strings.ToLower(decoded), "allowallfiles") {
		return errors.New("allowAllFiles is not permitted in metric_db_dsn")
	}
	resolved := metricstore.ResolveDriverFromConfig(driver, dsn)
	if resolved != metric.DriverSQLite {
		return nil
	}
	for i := 0; i < len(dsn); i++ {
		if dsn[i] < 0x20 || dsn[i] == 0x7f {
			return errors.New("invalid SQLite path")
		}
	}
	if strings.Contains(dsn, "%") {
		return errors.New("percent-encoding is not permitted in SQLite metric_db_dsn")
	}
	dbPath := dsn
	if strings.HasPrefix(strings.ToLower(dbPath), "file:") {
		dbPath = dbPath[5:]
		if strings.HasPrefix(dbPath, "//") {
			return errors.New("URI authority is not permitted in SQLite metric_db_dsn")
		}
	}
	if idx := strings.IndexByte(dbPath, '#'); idx >= 0 {
		dbPath = dbPath[:idx]
	}
	if idx := strings.IndexByte(dbPath, '?'); idx >= 0 {
		dbPath = dbPath[:idx]
	}
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" || dbPath == ":memory:" {
		return errors.New("SQLite metric_db_dsn must point to a file inside ./data/")
	}
	if strings.Contains(dbPath, "..") {
		return errors.New("SQLite path cannot contain '..'")
	}
	for _, seg := range strings.Split(strings.ReplaceAll(dbPath, "\\", "/"), "/") {
		if seg == ".." {
			return errors.New("SQLite path cannot contain '..'")
		}
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return errors.New("failed to determine working directory")
	}
	dataDir, err := evalSymlinksClosest(filepath.Join(workingDir, "data"))
	if err != nil {
		return errors.New("failed to resolve data directory")
	}
	target := dbPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(workingDir, target)
	}
	absTarget, err := evalSymlinksClosest(target)
	if err != nil {
		return errors.New("invalid SQLite path")
	}
	rel, err := filepath.Rel(dataDir, absTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("SQLite metric_db_dsn must reside inside ./data/")
	}
	baseLower := strings.ToLower(filepath.Base(absTarget))
	if baseLower == "lite-instance.key" {
		return errors.New("SQLite metric_db_dsn cannot target reserved system files")
	}
	for _, forbidden := range forbiddenSQLiteTargets(workingDir, dataDir) {
		if strings.EqualFold(absTarget, forbidden) {
			return errors.New("SQLite metric_db_dsn cannot target the main database or reserved files")
		}
	}
	return nil
}

var humanSessionSettingKeys = []string{
	config.CustomHeadKey,
	config.CustomBodyKey,
	config.ThemeKey,
	config.DisablePasswordLoginKey,
	config.OAuthEnabledKey,
	config.OAuthProviderKey,
	config.ApiKeyKey,
	config.CorsOriginCheckEnabledKey,
	config.CorsAllowedOriginsKey,
	config.WsOriginCheckEnabledKey,
	config.WsAllowedOriginsKey,
	config.AllowRemoteManagementKey,
	config.AllowMCPKey,
	"metric_db_type",
	metricstore.MetricDBDriverKey,
	metricstore.MetricDBDSNKey,
}

func settingsRequireHumanSession(cfg map[string]interface{}) bool {
	for _, key := range humanSessionSettingKeys {
		if _, ok := cfg[key]; ok {
			return true
		}
	}
	return false
}

func adminOrderClients(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var order map[string]int
	if err := req.BindParams(&order); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid or missing request body: "+err.Error(), nil)
	}
	if err := clients.UpdateClientOrder(order); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to update client weight: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "info", "audit.order_clients", nil)
	return nil, nil
}
