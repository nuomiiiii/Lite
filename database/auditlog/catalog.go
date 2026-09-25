package auditlog

import (
	"strconv"
	"strings"
)

// settingLabels, fileOpLabels, phrases, and noneLabels must match the four
// admin locales. TestCatalogMatchesAdminLocales checks Lite-web when it sits
// next to this module. The log page translates through the locale keys; these
// tables only let search match the on-screen words.
var settingLabels = map[string][4]string{
	"admin_default_page_size":          {"列表默认分页", "Default list pagination", "リストの既定ページ件数", "預設每頁筆數"},
	"allow_mcp":                        {"启用 MCP 代理", "Enable MCP Proxy", "MCP プロキシを有効にする", "啟用 MCP 服務"},
	"allow_remote_management":          {"允许远程管理", "Allow remote management", "リモート管理を許可する", "啟用遠端管理"},
	"api_key":                          {"站点 API 密钥", "Site API key", "サイト API キー", "API 金鑰"},
	"auto_order_new_clients_by_region": {"新增服务器自动排序", "Auto-order new servers", "新規サーバーの自動並び替え", "新伺服器自動排列"},
	"cloudflare_tunnel_token":          {"Cloudflare Tunnel 令牌", "Cloudflare Tunnel Token", "Cloudflare Tunnel トークン", "Cloudflare Tunnel Token"},
	"cors_allowed_origins":             {"允许的 Origin", "Allowed origins", "許可する Origin", "允許的來源"},
	"cors_origin_check_enabled":        {"CORS 跨域请求校验", "CORS origin check", "CORS オリジンチェック", "CORS 跨域請求驗證"},
	"custom_body":                      {"自定义 Body", "Custom body", "カスタム Body", "自訂 Body"},
	"custom_head":                      {"自定义 Head", "Custom head", "カスタム Head", "自訂 Head"},
	"description":                      {"站点描述", "Site Description", "サイトの説明", "網站描述"},
	"disable_password_login":           {"禁止密码登录", "Disable password login", "パスワードログインを禁止", "停用密碼登入功能"},
	"expire_notification_enabled":      {"是否启用过期提醒", "Enable Expiration Reminder", "期限切れ通知を有効にしますか", "啟用伺服器續約提醒"},
	"expire_notification_lead_days":    {"提前多少天开始提醒", "Days in Advance to Remind", "何日前から通知を開始しますか", "提早通知天數"},
	"geo_ip_enabled":                   {"启用地理位置信息", "Enable GeoIP", "地理位置情報を有効にする", "顯示地理位置資訊"},
	"geo_ip_provider":                  {"地理位置数据提供商", "GeoIP Provider", "地理位置データプロバイダー", "選擇 GeoIP 服務商"},
	"https_certificate_path":           {"证书路径", "Certificate path", "証明書のパス", "憑證路徑"},
	"https_enabled":                    {"启用内置 HTTPS", "Enable built-in HTTPS", "内蔵 HTTPS を有効化", "啟用內建 HTTPS"},
	"https_listen":                     {"HTTPS 监听端口", "HTTPS listening port", "HTTPS 待受ポート", "HTTPS 監聽 Port"},
	"https_private_key_path":           {"私钥路径", "Private key path", "秘密鍵のパス", "私鑰路徑"},
	"https_redirect_http":              {"HTTP 自动跳转 HTTPS", "Redirect HTTP to HTTPS", "HTTP から HTTPS へ自動転送", "HTTP 自動轉址至 HTTPS"},
	"login_notification":               {"登录通知", "Login Notification", "ログイン通知", "登入通知"},
	"mcp_default_duration_minutes":     {"默认授权时长", "Default duration", "デフォルト期間", "預設授權時效"},
	"mcp_max_concurrency":              {"每次授权最大并发", "Max concurrent operations per authorization", "承認あたりの最大同時実行数", "單次授權最多可同時執行"},
	"mcp_max_duration_minutes":         {"最长授权时长", "Maximum duration", "最大期間", "授權時效上限"},
	"metric_db_dsn":                    {"连接串（DSN）", "Connection string (DSN)", "接続文字列（DSN）", "連線字串（DSN）"},
	"metric_max_idle_conns":            {"最大空闲连接数", "Max idle connections", "最大アイドル接続数", "最大閒置連線數"},
	"metric_max_open_conns":            {"最大连接数", "Max open connections", "最大接続数", "最大連線數"},
	"metric_table_prefix":              {"表名前缀", "Table prefix", "テーブル接頭辞", "資料表 Prefix"},
	"notification_enabled":             {"开启通知", "Enable Notifications", "通知を有効にする", "開啟通知"},
	"notification_method":              {"通知渠道", "Delivery Method", "通知チャネル", "通知管道"},
	"notification_template":            {"消息通知模板", "Notification template", "メッセージ通知テンプレート", "訊息通知範本"},
	"o_auth_enabled":                   {"启用单点登录", "Enable Single Sign-On", "シングルサインオンを有効にする", "啟用 SSO"},
	"o_auth_provider":                  {"单点登录提供商", "Single Sign-On Provider", "シングルサインオンプロバイダー", "SSO Provider"},
	"private_site":                     {"私有站点", "Private site", "プライベートサイト", "限制公開存取"},
	"script_domain":                    {"安装命令中的站点地址", "Site address in install commands", "インストールコマンドのサイトアドレス", "Agent 服務網址"},
	"send_ip_addr_to_guest":            {"向访客显示部分IP地址", "Show partial IP address to guests", "ゲストに一部のIPアドレスを表示", "向訪客顯示部分 IP 位址"},
	"session_ttl_seconds":              {"自动登出", "Auto sign-out", "自動サインアウト", "自動登出"},
	"sitename":                         {"站点名称", "Site Name", "サイト名", "網站名稱"},
	"tempory_share_token":              {"临时分享", "Temporary Share", "一時的な共有", "訪客連結"},
	"theme":                            {"外观与主题", "Appearance & Themes", "外観とテーマ", "外觀與主題"},
	"traffic_limit_percentage":         {"流量用量", "Traffic Usage", "トラフィック使用量", "傳輸用量提醒"},
	"traffic_report_time":              {"报告推送时间", "Report Delivery Time", "レポート送信時刻", "報告傳送時間"},
	"ws_allowed_origins":               {"WebSocket Origin 允许列表", "WebSocket allowed origins", "WebSocket オリジン許可リスト", "WebSocket 來源允許清單"},
	"ws_origin_check_enabled":          {"WebSocket Origin 校验", "WebSocket origin check", "WebSocket オリジンチェック", "WebSocket 來源驗證"},
}

var noneLabels = [4]string{"无", "None", "なし", "無"}

var fileOpLabels = map[string][4]string{
	"file.copy":         {"复制文件", "copy a file", "ファイルのコピー", "複製檔案"},
	"file.create":       {"新建文件", "create a file", "ファイルの作成", "新增檔案"},
	"file.delete":       {"删除文件", "delete a file", "ファイルの削除", "刪除檔案"},
	"file.mkdir":        {"新建文件夹", "create a folder", "フォルダの作成", "新增資料夾"},
	"file.rename":       {"重命名文件", "rename a file", "ファイル名の変更", "重新命名檔案"},
	"file.upload.start": {"上传文件", "upload a file", "ファイルのアップロード", "上傳檔案"},
}

var phrases = map[string][4]string{
	"audit.avatar_remove":           {"移除了账户头像", "Removed the account avatar", "アカウント画像を削除しました", "移除了帳戶頭像"},
	"audit.avatar_update":           {"更新了账户头像", "Updated the account avatar", "アカウント画像を更新しました", "更新了帳戶頭像"},
	"audit.billing_ip":              {"记录了服务器「{{name}}」的 IP 变更费用 {{amount}} {{currency}}", "Recorded an IP change cost of {{amount}} {{currency}} for {{name}}", "サーバー「{{name}}」の IP 変更費用 {{amount}} {{currency}} を記録しました", "記錄了伺服器「{{name}}」的 IP 變更費用 {{amount}} {{currency}}"},
	"audit.billing_once":            {"记录了服务器「{{name}}」的一次性费用 {{amount}} {{currency}}", "Recorded a one-time fee of {{amount}} {{currency}} for {{name}}", "サーバー「{{name}}」の一回限りの費用 {{amount}} {{currency}} を記録しました", "記錄了伺服器「{{name}}」的一次性費用 {{amount}} {{currency}}"},
	"audit.billing_reset":           {"记录了服务器「{{name}}」的流量重置费用 {{amount}} {{currency}}", "Recorded a traffic reset cost of {{amount}} {{currency}} for {{name}}", "サーバー「{{name}}」のトラフィックリセット費用 {{amount}} {{currency}} を記録しました", "記錄了伺服器「{{name}}」的傳輸重置費用 {{amount}} {{currency}}"},
	"audit.billing_void":            {"作废了账单记录 #{{id}}", "Voided billing entry #{{id}}", "請求記録 #{{id}} を無効にしました", "作廢了帳單紀錄 #{{id}}"},
	"audit.bool_off":                {"关闭了「{{setting}}」", "Turned off {{setting}}", "「{{setting}}」をオフにしました", "關閉了「{{setting}}」"},
	"audit.bool_on":                 {"开启了「{{setting}}」", "Turned on {{setting}}", "「{{setting}}」をオンにしました", "開啟了「{{setting}}」"},
	"audit.client_create":           {"添加了服务器「{{name}}」", "Added server {{name}}", "サーバー「{{name}}」を追加しました", "新增了伺服器「{{name}}」"},
	"audit.client_delete":           {"删除了服务器「{{name}}」", "Deleted server {{name}}", "サーバー「{{name}}」を削除しました", "刪除了伺服器「{{name}}」"},
	"audit.client_edit":             {"修改了服务器「{{name}}」", "Edited server {{name}}", "サーバー「{{name}}」を変更しました", "修改了伺服器「{{name}}」"},
	"audit.client_token_rotate":     {"更换了服务器「{{name}}」的连接令牌", "Rotated the connection token for {{name}}", "サーバー「{{name}}」の接続トークンを差し替えました", "更換了伺服器「{{name}}」的連線權杖"},
	"audit.client_token_view":       {"查看了服务器「{{name}}」的连接令牌", "Viewed the connection token for {{name}}", "サーバー「{{name}}」の接続トークンを表示しました", "查看了伺服器「{{name}}」的連線權杖"},
	"audit.clipboard_batch_delete":  {"删除了 {{count}} 条命令剪贴板", "Deleted {{count}} command clipboard items", "コマンドクリップボードを {{count}} 件削除しました", "刪除了 {{count}} 筆指令剪貼簿"},
	"audit.clipboard_create":        {"添加了命令剪贴板 #{{id}}", "Added command clipboard #{{id}}", "コマンドクリップボード #{{id}} を追加しました", "新增了指令剪貼簿 #{{id}}"},
	"audit.clipboard_delete":        {"删除了命令剪贴板 #{{id}}", "Deleted command clipboard #{{id}}", "コマンドクリップボード #{{id}} を削除しました", "刪除了指令剪貼簿 #{{id}}"},
	"audit.clipboard_update":        {"修改了命令剪贴板 #{{id}}", "Updated command clipboard #{{id}}", "コマンドクリップボード #{{id}} を変更しました", "修改了指令剪貼簿 #{{id}}"},
	"audit.cloudflare_start":        {"启动了 Cloudflare Tunnel", "Started Cloudflare Tunnel", "Cloudflare Tunnel を起動しました", "啟動了 Cloudflare Tunnel"},
	"audit.cloudflare_stop":         {"停止了 Cloudflare Tunnel", "Stopped Cloudflare Tunnel", "Cloudflare Tunnel を停止しました", "停止了 Cloudflare Tunnel"},
	"audit.cloudflare_token_remove": {"清除了 Cloudflare Tunnel 令牌", "Removed the Cloudflare Tunnel token", "Cloudflare Tunnel トークンを削除しました", "清除了 Cloudflare Tunnel Token"},
	"audit.dashboard_update":        {"更新了仪表盘设置", "Updated dashboard settings", "ダッシュボード設定を更新しました", "更新了儀表板設定"},
	"audit.database_reclaim":        {"整理了数据库空间", "Reclaimed database space", "データベースの空き領域を回収しました", "整理了資料庫空間"},
	"audit.database_reclaim_error":  {"整理数据库空间时出现错误", "Database space reclaim finished with errors", "データベースの空き領域回収でエラーが発生しました", "整理資料庫空間時發生錯誤"},
	"audit.deployment_save":         {"保存了服务器「{{name}}」的安装配置", "Saved the install profile for {{name}}", "サーバー「{{name}}」のインストール設定を保存しました", "儲存了伺服器「{{name}}」的安裝設定"},
	"audit.event_fail":              {"事件「{{event}}」通知发送失败：{{error}}", "Failed to send the notification for {{event}}: {{error}}", "イベント「{{event}}」の通知送信に失敗しました：{{error}}", "事件「{{event}}」通知傳送失敗：{{error}}"},
	"audit.event_ok":                {"已发送事件通知「{{event}}」", "Sent the notification for {{event}}", "イベント「{{event}}」の通知を送信しました", "已傳送事件通知「{{event}}」"},
	"audit.favicon_restore":         {"恢复了默认站点图标", "Restored the default site icon", "既定のサイトアイコンに戻しました", "還原了預設網站圖示"},
	"audit.favicon_upload":          {"更新了站点图标", "Updated the site icon", "サイトアイコンを更新しました", "更新了網站圖示"},
	"audit.geoip_update":            {"更新了地理位置数据库", "Updated the GeoIP database", "地理位置データベースを更新しました", "更新了地理位置資料庫"},
	"audit.https_reload":            {"重新加载了内置 HTTPS 证书", "Reloaded the built-in HTTPS certificate", "内蔵 HTTPS 証明書を再読み込みしました", "重新載入了內建 HTTPS 憑證"},
	"audit.https_update":            {"更新了内置 HTTPS 设置", "Updated built-in HTTPS settings", "内蔵 HTTPS 設定を更新しました", "更新了內建 HTTPS 設定"},
	"audit.login_oauth":             {"通过单点登录", "Signed in with single sign-on", "シングルサインオンでログインしました", "透過 SSO 登入後台"},
	"audit.login_passkey":           {"通过通行密钥登录", "Signed in with a passkey", "パスキーでログインしました", "透過通行密鑰登入後台"},
	"audit.login_password":          {"通过密码登录", "Signed in with a password", "パスワードでログインしました", "透過密碼登入後台"},
	"audit.logout":                  {"退出了登录", "Signed out", "ログアウトしました", "已登出"},
	"audit.mcp_approve":             {"批准了 MCP 授权 {{id}}", "Approved MCP authorization {{id}}", "MCP 認可 {{id}} を承認しました", "核准了 MCP 授權 {{id}}"},
	"audit.mcp_deny":                {"拒绝了 MCP 授权 {{id}}", "Denied MCP authorization {{id}}", "MCP 認可 {{id}} を拒否しました", "拒絕了 MCP 授權 {{id}}"},
	"audit.mcp_terminal_close":      {"断开了服务器「{{name}}」的 MCP 终端，持续 {{duration}}", "Disconnected the MCP terminal for {{name}} after {{duration}}", "サーバー「{{name}}」の MCP ターミナルを切断しました（{{duration}}）", "中斷了伺服器「{{name}}」的 MCP 終端，持續 {{duration}}"},
	"audit.mcp_terminal_open":       {"建立了服务器「{{name}}」的 MCP 终端", "Opened an MCP terminal for {{name}}", "サーバー「{{name}}」の MCP ターミナルを開始しました", "建立了伺服器「{{name}}」的 MCP 終端"},
	"audit.message_fail":            {"通知「{{title}}」发送失败：{{error}}", "Failed to send notification {{title}}: {{error}}", "通知「{{title}}」の送信に失敗しました：{{error}}", "通知「{{title}}」傳送失敗：{{error}}"},
	"audit.message_ok":              {"已发送通知「{{title}}」", "Sent notification {{title}}", "通知「{{title}}」を送信しました", "已傳送通知「{{title}}」"},
	"audit.metric_cancel_migration": {"取消了监控数据库迁移", "Cancelled the metrics database migration", "監視データベースの移行を取り消しました", "取消了監控資料庫遷移"},
	"audit.metric_definition":       {"更新了监控项「{{name}}」", "Updated metric {{name}}", "監視項目「{{name}}」を更新しました", "更新了監控項目「{{name}}」"},
	"audit.metric_init_fail":        {"初始化监控数据库失败：{{error}}", "Failed to initialize the metrics database: {{error}}", "監視データベースの初期化に失敗しました：{{error}}", "初始化監控資料庫失敗：{{error}}"},
	"audit.metric_oneshot_fail":     {"监控数据库迁移失败：{{error}}", "Metrics database migration failed: {{error}}", "監視データベースの移行に失敗しました：{{error}}", "監控資料庫遷移失敗：{{error}}"},
	"audit.metric_start_migration":  {"开始迁移监控数据库", "Started the metrics database migration", "監視データベースの移行を開始しました", "開始遷移監控資料庫"},
	"audit.metric_startup_fail":     {"启动时迁移监控数据失败：{{error}}", "Metrics migration at startup failed: {{error}}", "起動時の監視データ移行に失敗しました：{{error}}", "啟動時遷移監控資料失敗：{{error}}"},
	"audit.mmdb_dir":                {"创建地理位置数据库目录失败：{{error}}", "Failed to create the GeoIP data directory: {{error}}", "地理位置データベースのディレクトリ作成に失敗しました：{{error}}", "建立地理位置資料庫目錄失敗：{{error}}"},
	"audit.mmdb_download":           {"下载地理位置数据库失败：{{error}}", "Failed to download the GeoIP database: {{error}}", "地理位置データベースのダウンロードに失敗しました：{{error}}", "下載地理位置資料庫失敗：{{error}}"},
	"audit.mmdb_init":               {"初始化地理位置数据库失败：{{error}}", "Failed to initialize the GeoIP database: {{error}}", "地理位置データベースの初期化に失敗しました：{{error}}", "初始化地理位置資料庫失敗：{{error}}"},
	"audit.oauth_init_fail":         {"初始化单点登录失败：{{error}}", "Failed to initialize single sign-on: {{error}}", "シングルサインオンの初期化に失敗しました：{{error}}", "初始化 SSO 失敗：{{error}}"},
	"audit.oidc_load_fail":          {"加载单点登录提供商失败：{{error}}", "Failed to load the single sign-on provider: {{error}}", "シングルサインオン提供元の読み込みに失敗しました：{{error}}", "載入 SSO 提供商失敗：{{error}}"},
	"audit.order_clients":           {"调整了服务器顺序", "Reordered servers", "サーバーの並び順を変更しました", "調整了伺服器順序"},
	"audit.passkey_add":             {"添加了通行密钥「{{name}}」", "Added passkey {{name}}", "パスキー「{{name}}」を追加しました", "新增了通行密鑰「{{name}}」"},
	"audit.passkey_remove":          {"移除了一把通行密钥", "Removed a passkey", "パスキーを削除しました", "移除了一把通行密鑰"},
	"audit.records_clear":           {"清空了记录", "Cleared records", "記録を消去しました", "清空了紀錄"},
	"audit.records_clear_all":       {"清空了记录和延迟监测数据", "Cleared records and latency data", "記録と遅延監視データを消去しました", "清空了紀錄與延遲監測資料"},
	"audit.remote_close":            {"断开了服务器「{{name}}」的远程会话，持续 {{duration}}", "Disconnected the remote session for {{name}} after {{duration}}", "サーバー「{{name}}」のリモートセッションを切断しました（{{duration}}）", "中斷了伺服器「{{name}}」的遠端工作階段，持續 {{duration}}"},
	"audit.remote_file":             {"在服务器「{{name}}」上请求了{{operation}}", "Requested to {{operation}} on {{name}}", "サーバー「{{name}}」で{{operation}}を要求しました", "在伺服器「{{name}}」上要求了{{operation}}"},
	"audit.remote_open":             {"建立了服务器「{{name}}」的远程会话", "Opened a remote session for {{name}}", "サーバー「{{name}}」のリモートセッションを開始しました", "建立了伺服器「{{name}}」的遠端工作階段"},
	"audit.remote_request":          {"请求连接服务器「{{name}}」的远程会话", "Requested a remote session for {{name}}", "サーバー「{{name}}」へのリモートセッションを要求しました", "要求連線伺服器「{{name}}」的遠端工作階段"},
	"audit.renewal_fail":            {"自动续费服务器「{{name}}」失败：{{error}}", "Failed to auto-renew {{name}}: {{error}}", "サーバー「{{name}}」の自動更新に失敗しました：{{error}}", "自動續約伺服器「{{name}}」失敗：{{error}}"},
	"audit.renewal_ok":              {"已自动续费服务器「{{name}}」，到期日 {{until}}", "Auto-renewed {{name}} until {{until}}", "サーバー「{{name}}」を {{until}} まで自動更新しました", "已自動續約伺服器「{{name}}」，到期日 {{until}}"},
	"audit.secret_clear":            {"清空了「{{setting}}」", "Cleared {{setting}}", "「{{setting}}」を消去しました", "清空了「{{setting}}」"},
	"audit.secret_replace":          {"更换了「{{setting}}」", "Replaced {{setting}}", "「{{setting}}」を差し替えました", "更換了「{{setting}}」"},
	"audit.secret_set":              {"设置了「{{setting}}」", "Set {{setting}}", "「{{setting}}」を設定しました", "設定了「{{setting}}」"},
	"audit.self_update":             {"安排了更新到 {{version}}（{{hash}}）", "Scheduled an update to {{version}} ({{hash}})", "{{version}}（{{hash}}）への更新を予約しました", "已排程更新至 {{version}}（{{hash}}）"},
	"audit.server_fatal":            {"服务遇到严重错误：{{error}}", "The server hit a fatal error: {{error}}", "サーバーで致命的なエラーが発生しました：{{error}}", "服務發生嚴重錯誤：{{error}}"},
	"audit.server_shutdown":         {"服务正在关闭", "The server is shutting down", "サーバーを停止しています", "服務正在關閉"},
	"audit.session_delete":          {"删除了一条登录会话", "Deleted a sign-in session", "ログインセッションを削除しました", "刪除了一個登入工作階段"},
	"audit.session_delete_all":      {"删除了全部登录会话", "Deleted every sign-in session", "すべてのログインセッションを削除しました", "刪除了全部登入工作階段"},
	"audit.sso_bind":                {"绑定了外部账户", "Linked an external account", "外部アカウントを連携しました", "綁定了外部帳戶"},
	"audit.sso_passkey":             {"通过单点登录确认了账户，以便使用通行密钥", "Confirmed the account with single sign-on for a passkey", "パスキー利用のためシングルサインオンでアカウントを確認しました", "透過 SSO 確認了帳戶，以便使用通行密鑰"},
	"audit.terminal_task":           {"下发了远程命令，任务 {{id}}", "Sent a remote command, task {{id}}", "リモートコマンドを送信しました。タスク {{id}}", "下發了遠端指令，工作 {{id}}"},
	"audit.text_update":             {"更新了「{{setting}}」", "Updated {{setting}}", "「{{setting}}」を更新しました", "更新了「{{setting}}」"},
	"audit.traffic_calibrate":       {"校准了服务器「{{name}}」的本周期流量，上行 {{up}}，下行 {{down}}", "Calibrated this cycle's traffic for {{name}}: up {{up}}, down {{down}}", "サーバー「{{name}}」の今周期トラフィックを校正しました。上り {{up}}、下り {{down}}", "校正了伺服器「{{name}}」本週期傳輸用量，上行 {{up}}，下行 {{down}}"},
	"audit.user_update":             {"更新了账户资料", "Updated the account profile", "アカウント情報を更新しました", "更新了帳戶資料"},
	"audit.value_change":            {"将「{{setting}}」从「{{from}}」改为「{{to}}」", "Changed {{setting}} from {{from}} to {{to}}", "「{{setting}}」を「{{from}}」から「{{to}}」に変更しました", "將「{{setting}}」從「{{from}}」改為「{{to}}」"},
	"audit.visitor":                 {"访客事件 {{detail}}", "Visitor event {{detail}}", "訪問者イベント {{detail}}", "訪客事件 {{detail}}"},
	"audit.xterm_update":            {"更新了终端设置", "Updated terminal settings", "ターミナル設定を更新しました", "更新了終端機設定"},
}

func searchText(key string, params map[string]string) string {
	lines := make([]string, 0, 4)
	for lang := 0; lang < 4; lang++ {
		line := phrase(lang, key, params)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func phrase(lang int, key string, params map[string]string) string {
	template, ok := phrases[key]
	if !ok {
		return ""
	}
	return fill(template[lang], localizedParams(lang, params))
}

func localizedParams(lang int, params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for key, value := range params {
		out[key] = value
	}
	setting := params["setting"]
	if setting != "" {
		out["setting"] = settingLabel(lang, setting)
		if value, ok := params["from"]; ok {
			out["from"] = displayValue(lang, setting, value)
		}
		if value, ok := params["to"]; ok {
			out["to"] = displayValue(lang, setting, value)
		}
	}
	if op := params["operation"]; op != "" {
		out["operation"] = fileOpLabel(lang, op)
	}
	return out
}

func settingLabel(lang int, key string) string {
	labels, ok := settingLabels[key]
	if !ok || strings.TrimSpace(labels[lang]) == "" {
		return key
	}
	return labels[lang]
}

func fileOpLabel(lang int, op string) string {
	labels, ok := fileOpLabels[op]
	if !ok {
		return op
	}
	return labels[lang]
}

func displayValue(lang int, setting, raw string) string {
	switch setting {
	case "https_listen":
		return strings.TrimPrefix(raw, ":")
	case "geo_ip_provider":
		switch raw {
		case "", "empty":
			return noneLabels[lang]
		case "mmdb":
			return "MaxMind"
		case "ip-api":
			return "ip-api.com"
		case "geojs":
			return "geojs.io"
		case "ipinfo":
			return "ipinfo.io"
		}
	case "session_ttl_seconds":
		if formatted := formatTTL(lang, raw); formatted != "" {
			return formatted
		}
	}
	return raw
}

func formatTTL(lang int, raw string) string {
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return ""
	}
	var count int
	var unit [4]string
	switch {
	case seconds%86400 == 0:
		count = seconds / 86400
		unit = [4]string{"天", "d", "日", "天"}
	case seconds%3600 == 0:
		count = seconds / 3600
		unit = [4]string{"时", "h", "時間", "時"}
	case seconds%60 == 0:
		count = seconds / 60
		unit = [4]string{"分", "m", "分", "分"}
	default:
		return ""
	}
	return strconv.Itoa(count) + unit[lang]
}

func fill(template string, params map[string]string) string {
	out := template
	for key, value := range params {
		out = strings.ReplaceAll(out, "{{"+key+"}}", value)
	}
	return out
}
